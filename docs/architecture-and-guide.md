# MCP Language Server 架构与扩展指南

> 刷新版，2026-07-17，基于 commit `be40c6f`（main）。
> 2026-07-20 更新：工具面改为条件注册——默认 7 个只读工具，`edit_file`/`rename_symbol` 由 `MCP_LS_ENABLE_EDITS` 门控（§3.2）；缓存按文件失效走反向索引（§3.7）。
> 本文取代旧版《架构深度分析与使用指南》：旧文描述的"17 工具全量注册 + searchAll 三层拼接"架构已被重写，当前为"条件注册工具面 + CodeAtom IR 归一化管道"。
> 配套文档：工具参考《docs/MCP_TOOLS_ZH.md》、优化方案《docs/tools-opt.md》、IR 设计《docs/code-atom-ir.md》、设计思路深入解析《docs/design-deep-dive.md》、压测《docs/benchmark-2026-07-17.md》、待办《docs/optimization-backlog.md》。

---

## 1. 项目定位

向 LLM 暴露代码上下文提取能力的 MCP server（Go 1.24，stdio/NDJSON JSON-RPC）。核心场景：**百万级 C/C++ 代码库（3GPP 级复杂度、多单板编译宏控制）的安全漏洞检视**，同时兼容 Go/Rust/Python/TypeScript（取决于接入的 LSP server）。

启动：

```bash
mcp-language-server --workspace <repo> --lsp clangd [-- <lsp-args>]
```

部署模式（2026-07-20 起）：

| 模式 | 命令 | 适用 |
|------|------|------|
| 独立 stdio（默认） | `mcp-language-server --workspace X --lsp Y` | 单客户端进程；1 客户端 = 1 本进程 = 1 份 LSP |
| daemon + proxy | `mcp-language-server proxy --workspace X --lsp Y`（daemon 自动拉起，或手动 `daemon` 子命令） | 多进程客户端共享同一 workspace：全部客户端共用 1 份 LSP/watcher/搜索缓存，避免 N 份 clangd 导致 OOM。设计与验证见《docs/daemon-proxy-design.md》 |

环境变量：

| 变量 | 作用 | 默认 |
|------|------|------|
| `MCP_LS_DEBUG_TOOLS` | 非空时追加注册 8 个 debug 工具 | 不注册 |
| `MCP_LS_ENABLE_EDITS` | 非空时注册 `edit_file`/`rename_symbol` | 不注册（只读检视面） |
| `MCP_LS_CACHE_TTL` | 搜索缓存 TTL（秒） | 300 |

---

## 2. 总体架构

```
LLM (MCP client)
   │ stdio / NDJSON JSON-RPC
   ▼
┌─────────────────────────────────────────────────────────┐
│ tools.go  registerTools()                                │
│   7 个默认只读工具（检视面）                              │
│   +8 debug 工具（MCP_LS_DEBUG_TOOLS）                    │
│   +2 编辑工具（MCP_LS_ENABLE_EDITS）                     │
│   search ──────────────┐                                │
│   definition/references/callers/callees/diagnostics/    │
│   hover                                               │
└────────────────────────┼────────────────────────────────┘
                         ▼
              internal/tools/router (Router)
              strategy ∈ {auto, text, ast, symbol}
              auto+intent → searchLayerUnified（单层归一化）
              auto 无intent → searchAll（三层并发 → 归一化）
                         │
        ┌────────────────┼─────────────────┐
        ▼                ▼                 ▼
   L1: ripgrep     L2: tree-sitter    L3: LSP client (clangd/gopls/...)
   SearchCode(Matches)  QueryDirectory    Symbol/Definition/CallHierarchy
        │                │                 │
        └────────────────┼─────────────────┘
                         ▼
              internal/tools/atom (CodeAtom IR)
              归一化 → MergePhysical（吞并+折叠）→ DedupSemantic
              → CropBudget（L0→L1→L2→丢弃）→ Render
                         │
        横切：cache（TTL + 按文件失效）｜ includemap（include 邻域限定）
        watcher（fsnotify，300ms 防抖）→ Router.InvalidateFile
```

关键设计取舍：**LLM 面对的选择题收敛到默认 7 选 1（只读检视面）**，其中约 80% 场景应由 `search` 承接；确定性导航（已知名字看定义/引用/调用链）走 L3 单点工具。编辑工具 `edit_file`/`rename_symbol` 默认不注册（`MCP_LS_ENABLE_EDITS` 开启）——安全检视场景下 LLM 误触发编辑是事故而非功能。

---

## 3. 模块详解

### 3.1 入口与生命周期（main.go）

- `parseConfig`：`--workspace`（必填）、`--lsp`（必填）、`--` 之后的 LSP 参数。
- `initializeLSP`：创建 `lsp.Client`（独立进程）→ `InitializeLSPClient` → 启动 watcher → `WaitForServerReady`（以 workspace/symbol 可应答为准）。
- `warmUpLSP`：**clangd 18 在收到 initialize 后不做后台索引，要等第一个 didOpen**。启动时自动打开 compile_commands.json 的首个翻译单元触发索引；无该文件则跳过（实测：符号层 6.8s 自动就位，修复前永远停在 ripgrep 降级态）。
- `registerTools` → `server.ServeStdio`；SIGINT/SIGTERM 时关闭 LSP 进程。

### 3.2 工具面（tools.go）

**注册机制**：按环境变量条件注册（tools.go 中 `if debugTools` / `if editsEnabled` 分组）。默认 7 个只读工具；`MCP_LS_DEBUG_TOOLS` 非空时追加 8 个 debug 工具（`search_text/search_ast/search_symbol/ripgrep/treesitter_query/treesitter_ast/find_struct_usage/find_struct_definition`——能力均被 `search`+strategy 覆盖，仅作调试逃生舱）；`MCP_LS_ENABLE_EDITS` 非空时追加 `edit_file`/`rename_symbol`。

**description 原则**：触发式（"当需要 X 时用我；不要用 Y 场景"），`search` 声明为默认首选入口，`definition`/`references` 定位为结果不精确时的精确化手段。

**数据/格式分离**：`callers`/`callees`/`diagnostics` 返回 `NewToolResultStructured(structured, fallbackText)`——结构化数据供 UI 渲染，文本供纯文本客户端；并带 `Meta.ui.resourceUri` 指向 MCP App 资源（见 §3.9）。

**参数硬限制**：`callers`/`callees` depth 默认 1、clamp 3（百万行仓调用图扇出防爆）；文本输出截断（callers 16KB，见 §3.4 预算矩阵）。

### 3.3 统一搜索（internal/tools/router/router.go，~830 行）

`SearchOptions{Query, Strategy, Intent, FilePath, Language, MaxCount, ContextLines, CaseSensitive, WholeWord}`；`SearchResult{Layer, Content, Count, Files}`（Files 为缓存失效依赖集，见 §3.7）。

**路由矩阵**：

| 调用 | 路径 | 输出 |
|------|------|------|
| `strategy=text/ast/symbol` | `searchText/searchAST/searchSymbol` | 原始单层格式，每层 ≤50 行/4KB 截断（逃生舱） |
| `strategy=auto` + intent 命中 | `searchLayerUnified` | 单层产出过 atom 管道，层标 `unified-text/ast/symbol` |
| `strategy=auto` 无 intent | `searchAll` | 三层并发 → atom 管道，层标 `unified` |

`routeByIntent`：中英文关键词（todo/注释/待办→text，function/结构体/函数→ast，definition/定义/引用/调用→symbol）。

`searchAll`：三个 goroutine 并发跑结构化生产者（见 §3.5），`wg.Wait()` 后**串行**归一化（文件读取带缓存，避免竞争）。任一层失败不阻断其他层；全部无产出返回 `No results found`。

**符号层降级**：LSP 无 client/出错/空结果 → ripgrep 降级，层标/警告明确标注（`symbol-fallback-text` 或 WARNING 头），带 filePath 锚点时按 include 邻域限定（§3.6）。

### 3.4 CodeAtom IR（internal/tools/atom/，~310 行）

`docs/code-atom-ir.md` 的 Phase 1 落地。`CodeAtom{SemanticID, Name, Kind, FilePath, StartByte, EndByte, FullContent(L0), Signature(L1), Reference(L2), SourceTool, Priority, MaxLevel, Level}`。

- **MergePhysical**（§3.1 扫描线）：同文件按 StartByte 排序。包含关系：容器为可折叠代码块（FUNCTION/STRUCT）时**父子折叠**——保留两者、容器 `MaxLevel` 降为 L1 骨架；否则吞并子级。部分交错（只可能来自异构源）：按 Priority 取舍。`StartByte<0`（坐标不明）豁免吞并。
- **DedupSemantic**（§3.2）：按 SemanticID 哈希去重，保留最高 Priority（symbol：`name@path`；AST：`path:nodetype:offset`；snippet：`path:offset`）。
- **CropBudget**（§4 四相降级背包）：Priority 降序，逐原子尝试 L0→L1→L2，从 `MaxLevel` 起试；**计费含渲染开销**（per-atom tag + per-file header），保证预算诚实。
- **Render**：统计头（原子总数/各层展示数/丢弃数/预算用量）+ 按文件分组 + 窄化提示。

**预算/截断矩阵**（上下文爆炸的四道闸）：

| 位置 | 上限 |
|------|------|
| atom 管道预算 | 8KB（含渲染开销计费，整体 <12KB） |
| 单层（显式 strategy） | 每层 50 行或 4KB |
| `formatSearchResults` 总量 | 12KB |
| callers/callees | depth ≤ 3，输出 16KB + 省略计数 |

### 3.5 三层引擎与结构化生产者

**L1 ripgrep**（internal/tools/ripgrep.go）：`SearchCode`（格式化文本）与 `SearchCodeMatches`（结构化 `TextMatch{Path,Line,Offset,Text}`）共用 `runRipgrep`。要点：解析 rg `--json` 的 `data` 包裹层（历史 bug，已修并补测试）；exit code 1（无匹配）不视为错误；`RipgrepOptions.Files` 支持显式文件集限定（include 邻域用）。router 侧 `snippetExpander` 把命中行 ±2 行扩展为 snippet 的 L0 载荷（带每文件行偏移缓存），相邻窗口经吞并自动合并。

**L2 tree-sitter**（internal/tools/treesitter/）：`parser.go`（c/cpp 解析器封装）、`query.go`（CSP 查询）。`QueryResult` 携带 `NodeType/StartByte/EndByte`（在 tree 存活期内填充，Node 指针不跨 Close 使用）。`QueryDirectory`：**worker pool 并发**（每 worker 独立 parser + 查询只编译一次），无效 CSP 模式预编译**失败快速返回**（u-boot 全仓 13k 文件扫描 2.8s）。auto 融合路径对非 CSP 查询静默跳过（自然语言 query 不是合法 CSP，不视为错误）。

**L3 LSP**（internal/lsp/ + internal/tools/definition|references|call_hierarchy 等）：
- `client.go`：进程管理（stdin/stdout 传输、消息分发、Call/Notify）、`OpenFile/NotifyChange/CloseFile`（增量同步）、诊断缓存（`GetFileDiagnostics`）、`WaitForServerReady`。
- `methods.go`：Symbol/Definition/Hover/References/DocumentSymbol/PrepareCallHierarchy/IncomingCalls/OutgoingCalls/Rename 等封装。
- `symbol_resolver.go`：symbolName → file:line:column（供 callers/callees 的 chat 式调用）。
- `GetFullDefinition`（lsp-utilities.go）：经 DocumentSymbol 定位外围符号并取实现体——symbol 原子 **Top-5** 的 L0 载荷来源（延迟护栏）。
- `call_hierarchy.go`：递归收集调用者/被调用者（seen 集合防环），`CallHierarchyData` 结构化 + 文本格式化（16KB 截断）。

### 3.6 include-map（internal/tools/includemap.go，~140 行）

解析 compile_commands.json 为双向映射（TU ↔ include 目录）。**被全仓所有 TU 使用的目录判定为无区分度并剔除**（u-boot 的 `include/` 即此类，否则全仓互为邻居）。

应用（消息带 filePath 锚点时）：
- symbol 降级 rg：限定到锚点的 include 邻域（≤400 文件）
- `strategy=text`：邻域限定（映射不到则限定文件本身）
- `strategy=ast`：邻域批量 tree-sitter（≤20 文件）
- 输出均带 `NOTE/WARNING (scoped to N files via include map)`

典型价值场景：多单板宏控制仓——clangd 只索引了当前 defconfig 的活跃分支，降级搜索限定到同编译族文件而不是全仓。

### 3.7 搜索缓存（internal/tools/cache/ + router）

- 键：`SearchCacheKey(query, strategy, filePath, language, intent)`（sha256 截 16 hex；intent 参与键值，杜绝串扰）。
- 值：`[]SearchResult`，TTL 默认 300s（`MCP_LS_CACHE_TTL` 可调）。
- **按文件失效**：缓存条目带文件依赖集（unified 结果从原子收集，**含被预算丢弃的贡献文件**）；watcher 变更 → `Router.InvalidateFile(uri)` 只失效依赖该文件的条目；无依赖信息的条目保守失效。`ClearCache()` 仍保留用于全量场景。
- 单次 Search 内缓存命中直接返回（含 unified 结果）。

### 3.8 watcher（internal/watcher/，~650 行）

fsnotify 监听工作区，gitignore 过滤，300ms 防抖（per-file timer）；事件同时经 `didChangeWatchedFiles` 通知 LSP（已打开文件走增量 `NotifyChange`），并回调 `OnFileChange(uri)` → 搜索缓存按文件失效。

### 3.9 UI 资源（ui_resources.go，MCP App）

两个 `text/html;profile=mcp-app` 资源：`ui://call-hierarchy/graph`、`ui://diagnostics/dashboard`。工具结果经 `postMessage` 把 `structuredContent` 注入内嵌 HTML 渲染（侧边栏统计 + 主面板列表/图）。纯文本客户端只看 fallbackText，互不影响。

### 3.10 其他

- `internal/protocol/`：LSP 类型（`tsprotocol.go` 为生成代码，`cmd/generate` 源自 gopls codegen）+ 接口适配（`interfaces.go` 的 `WorkspaceSymbolResult` 等）。
- `internal/logging/`：分组件日志（core/lsp/tools/watcher）。
- `internal/tools/utilities*.go`：行号格式化、文本编辑应用等杂项。

---

## 4. 数据流：一次 auto search 的完整旅程

以 u-boot（176 万行）上 `search(query="TODO")`（auto 无 intent）为例：

1. tools.go 解析参数 → `Router.Search`；缓存键含 intent（空）。
2. `searchAll` 三路并发：`SearchCodeMatches`（rg JSON）、`RunTreesitterQueryResults`（"TODO" 非合法 CSP → 静默无贡献）、`querySymbols`（"TODO" 非符号 → 空 → 触发 ripgrep 降级）。
3. 串行归一化：5569 条原始命中 → snippet 原子（±2 行扩展）+ 降级命中（SourceTool=rg(lsp-fallback)）。
4. `MergePhysical`（相邻 TODO 窗口合并）+ `DedupSemantic`（rg 与降级重复命中合并）→ **1683 原子**（原始命中的 30%）。
5. `CropBudget(8KB)`：Priority 降序装入，85 条 L0 展示，1598 条丢弃。
6. `Render` 统计头 + WARNING（LSP 降级标注）→ 单块 `unified` 结果（实测 7.9KB）。
7. `Router.Search` 记录文件依赖集（含被丢弃原子的文件）→ `SetWithFiles` 入缓存。

LLM 视角变化：修复前同查询返回 22.4KB 无截断三层拼接（且文本层全是 `0: ` 空行 bug）；现在是有统计、有降级标注、有窄化提示的单块结果。

---

## 5. 未来扩展性

### 5.1 已预留的扩展点

| 扩展点 | 位置 | 说明 |
|--------|------|------|
| 新原子源 | router 归一化段 | 新增 `atomsFromXxx` 生产者并入 `searchAll` 扇出即可，下游管道自动生效 |
| 新 LSP server | `--lsp` 参数 | 协议层无关；warmup 仅对 compile_commands.json 存在时生效，无侵入 |
| 新 UI 资源 | ui_resources.go | 工具返回 structured + `Meta.ui.resourceUri`，HTML 资源独立注册 |
| include-map 语义 | includemap.go | 可升级为"目录稀有度加权邻域"（当前为布尔共享） |
| 原子预算策略 | atom.CropBudget | Priority 评分函数可插拔（当前按源分层：clangd=3/tree-sitter=2/rg=1） |

### 5.2 CodeAtom IR 路线（对照 docs/code-atom-ir.md）

Phase 1 已落地：IR 结构、三层归一化、扫描线吞并、语义去重、四相预算（含父子折叠 §3.3、交错 Priority 取舍）、intent 路由统一化、snippet ±2 扩展、symbol Top-N L0 载荷。

未落地（backlog）：
- **USR 全局符号身份**（#9）：LSP 协议不暴露，需读 clangd 索引私有格式，高成本，暂缓。
- 其余 §3.3 之外的折叠策略（宏块折叠等）可按需扩展 `foldable()` 的 Kind 集合。

### 5.3 遗留决策项与压测扩展（docs/optimization-backlog.md）

- **P1-4 集成测试快照漂移**：go/hover、go/diagnostics、clangd 全套在干净基线上即失败（gopls/clangd-18 输出格式漂移，非代码问题）。需决策：固定 CI 工具版本后重生成快照。**勿在当前漂移环境直接 `UPDATE_SNAPSHOTS=true`**。
- **P3 压测仓**：真实单板 defconfig（需 ARM 交叉工具链）、OAI（构建脏，先验证 compile_commands）、linux 极限仓（慎入）。当前基线：u-boot 176 万行 / srsRAN 100 万行，见《docs/benchmark-2026-07-17.md》。

---

## 6. 测试与验证

```bash
go build ./... && go vet ./...
go test ./internal/...           # 单测（atom/router/cache/treesitter/ripgrep/call_hierarchy 等）
go test ./integrationtests/...   # 集成（需 gopls/clangd/rust-analyzer；快照见 §5.3 漂移说明）
```

单测覆盖：atom 三算法（吞并/折叠/去重/四相预算）、router 统一管道与降级邻域、include-map 无区分度规则、缓存按文件失效、rg JSON 解析、QueryDirectory 并发与快速失败、callers 截断。watcher 三个测试在本环境存在时序性失败（与代码改动无关，干净基线同样失败）。
