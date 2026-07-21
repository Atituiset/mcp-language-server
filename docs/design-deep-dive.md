# MCP Language Server 设计思路深入解析

> 2026-07-20，面向"想读懂这个仓为什么这么设计"的读者。
> 与《docs/architecture-and-guide.md》（参考手册式）不同，本文按**设计决策 → 流程追踪 → 算法细节 → 并发模型**的学习路径组织，所有结论都标注了代码位置（file:line，已对当前代码校准），可以对照源码验证。
> 文中性能数字均为 2026-07-20 在 u-boot（176 万行 C）+ clangd-18、本仓（2.3 万行 Go）+ gopls 上的实测。

---

## 0. 一句话定位

这个仓是一个 **MCP server**：把"代码库上下文提取"能力（搜索、定义、引用、调用链、诊断）通过 MCP 协议暴露给 LLM，**LLM 负责分析，它负责喂上下文**。

理解全部设计的前提是记住两个约束：

1. **LLM 的上下文窗口是最稀缺资源**。一次查询可能命中几千个位置，全塞给 LLM 等于拒绝服务。所以仓里最核心的子系统不是搜索，而是**结果压缩管道**（CodeAtom IR）。
2. **目标场景是百万行级、宏控多板的 C/C++ 仓**（u-boot 类）。语言服务器在这种仓上经常只有部分可用（clangd 只索引当前 defconfig 的活跃分支），所以**降级是一等设计**，不是异常处理。

---

## 1. 总图：一次查询的数据流

```
LLM (MCP client)
  │ stdio, NDJSON JSON-RPC
  ▼
tools.go  registerTools()          ← 工具面：默认 7 个只读工具
  │                                    （+8 debug / +2 编辑，env 门控）
  ▼
internal/tools/router (Router)     ← 路由：strategy/intent → 单层或三层并发
  │
  ├── L1: ripgrep（文本，最快，无语义）
  ├── L2: tree-sitter（AST，仅 C/C++，无语义符号表）
  └── L3: LSP client（clangd/gopls/...，最准，最慢，可能不可用）
  ▼
归一化为 atom.CodeAtom（统一中间表示）
  ▼
MergePhysical → DedupSemantic → CropBudget → Render   ← 核心管道
  ▼
单块带统计头的文本（≤ ~12KB），含降级标注和窄化提示
  ▼
LLM
```

横切支撑系统（不在主链路上但随时介入）：

- **watcher**（fsnotify）→ 文件变更 → LSP 增量同步 + 搜索缓存按文件失效
- **cache**（TTL + 反向索引）→ 相同查询直接短路
- **include map**（compile_commands.json）→ 把降级搜索限定到"同一个编译族"的文件
- **logging**（分组件）→ core/lsp/lsp-wire/lsp-process/tools/watcher 各自独立级别

---

## 2. 五个核心设计决策

### D1：把上下文窗口当预算来"花"——CodeAtom IR 管道

位置：`internal/tools/atom/atom.go`（约 310 行，全仓最精炼的文件）

三层引擎产出三种异构结果：rg 的行命中、tree-sitter 的 AST 节点、LSP 的符号。旧架构直接拼接，一次 `search("TODO")` 在 u-boot 上返回 22.4KB 无截断文本。新架构把它们**归一化成同一种结构**再统一处理：

```go
CodeAtom{
    SemanticID        // 语义身份：跨源去重的键
    Name, Kind        // FUNCTION/STRUCT/MACRO/SNIPPET/SYMBOL
    FilePath, StartByte, EndByte   // 空间身份：字节偏移是唯一事实
    FullContent       // L0 载荷：完整内容（最贵）
    Signature         // L1 载荷：签名/单行（中等）
    Reference         // L2 载荷：名字+位置（最便宜）
    SourceTool, Priority           // clangd=3 > tree-sitter=2 > rg=1
    MaxLevel, Level                // 预算器选择的展示级别
}
```

设计要点：**每个原子同时携带三级载荷**，预算器（CropBudget）按 Priority 降序决定每个原子以哪级载荷"上车"——重要的符号给全文，放不下的给签名，再给一行引用，实在不行丢弃。这本质是一个**三档降级背包**。

为什么这个设计对：LLM 拿到的不再是"前 N 条完整结果 + 后面全丢"，而是"重要结果完整 + 次要结果保留线索（名字和位置）"。被丢弃的原子也在统计头里报了数，LLM 知道该用 filePath 缩小范围再查。

### D2：三层引擎 + 单一入口——把 LLM 的选择题做小

位置：`internal/tools/router/router.go`

为什么不只留 LSP 层？因为三个层的**能力谱系互补**：

| 层 | 引擎 | 速度（u-boot 实测） | 能回答 | 不能回答 |
|----|------|------|--------|----------|
| L1 | ripgrep | 亚秒级 | "TODO 在哪"、"这个字符串在哪" | 语义（定义/引用） |
| L2 | tree-sitter | 秒级（13k 文件 2.8s） | "所有函数定义"、"这个结构体模式" | 跨文件、宏展开后的语义 |
| L3 | LSP | 秒~分钟级（依赖索引） | 定义/引用/调用链/诊断 | 索引未就绪时一切 |

LLM 不需要理解这个矩阵。入口只有一个 `search`（tools.go:505 注册），内部路由（`router.go:352`）：

- `strategy=text/ast/symbol`：显式指定，原样单层输出（调试逃生舱）；
- `strategy=auto` + `intent` 命中关键词（`routeByIntent`，router.go:807）：单层 + 归一化管道；
- `strategy=auto` 无 intent：**三层并发**（`searchAll`，router.go:474），结果全部进管道。

工具面收敛史也是设计思路的一部分：最初 17 个工具全量注册，LLM 选错率高；现在默认 **7 个只读工具**（search/definition/references/callers/callees/diagnostics/hover），其中约 80% 场景由 `search` 承接。debug 工具（`MCP_LS_DEBUG_TOOLS`）和编辑工具（`MCP_LS_ENABLE_EDITS`）走环境变量条件注册（tools.go:17-22）。

### D3：降级是一等公民，且必须"诚实标注"

这个仓最独特的工程态度：**任何一层失败都不让查询失败，但必须让 LLM 知道结果降级了**。

实例（全部实测验证过）：

- LSP 符号层不可用（进程死了/还没索引完/查不到）→ 降级 ripgrep，输出头部带 `WARNING: LSP unavailable...`；进程死亡时明确写 `LSP server process exited`（router.go:217 的 `lspFallbackWarning`）。
- intent 路由到 AST 层但 query 不是合法 CSP 模式（自然语言符号名）→ 降级文本搜索，带 `WARNING: query is not a valid tree-sitter CSP pattern`（router.go:389 的 `case "ast"` 分支）。*这条就是真实冒烟测试抓出来的：intent="function definition" + 符号名会 100% 触发。*
- 降级搜索在宏控仓里不能全仓乱搜 → include map 把范围限定到锚文件的"编译族邻域"，输出带 `NOTE: scoped to N file(s) via include map`（router.go:227-252，includemap.go）。

为什么强调标注：LLM 会基于结果做安全判断，把文本匹配当成语义结果是**分析事故**。降级可以，欺骗不行。

### D4：只读检视面——LLM 误编辑是事故

`edit_file` 和 `rename_symbol` 默认**不注册**（tools.go:23, 260），需要 `MCP_LS_ENABLE_EDITS=1` 显式开启。后端实现保留在 `internal/tools/`（集成测试继续覆盖），只是不进 LLM 的工具面。

同理，`ExecuteCodeLens` 这种能在仓库里跑 `go mod tidy` 的能力被直接删除了（连同集成测试）。

### D5：LSP 是重资产子进程——精细的并发与生命周期管理

LSP server（clangd/gopls）是独立子进程，通过 stdio 讲 JSON-RPC。围绕它有一整套管理代码（`internal/lsp/`）：

- **写串行化**：`writeMu` 保证 header+body 两次写不被并发写者交错（transport.go:194-204 的 `writeMessage`）。*修复前是潜伏的协议损坏 bug：工具 handler、watcher 回调、服务端请求回包三个写源并发。*
- **死亡检测**：读循环退出 → `markDead()` + 唤醒所有挂起请求（transport.go:100-111，client.go:124-157）。*修复前 LSP 崩溃后所有请求挂到超时——rust 集成测试在旧代码上挂 600 秒，现在 1.3 秒报明确错误。*
- **增量同步**：文件变更时算 prefix/suffix diff，按 LSP 默认的 UTF-16 编码换算位置发送 ranged change（client.go:470-546）。*修复前名为增量实为全量。*
- **打开文件状态**：`openFiles` map 记录版本号和最近同步内容，OpenFile 幂等（client.go:364-403）。

---

## 3. 关键流程追踪

### 3.1 `search(query="TODO")` —— auto 无 intent（u-boot 实测 0.6s）

1. tools.go:505 的 handler 解析参数 → `Router.Search`（router.go:92）。
2. 查缓存：key = sha256(query|strategy|filePath|language|intent) 前 16 hex。
3. `searchAuto` → 无 intent → `searchAll`（router.go:474）。
4. **三个 goroutine 并发**：
   - L1：`SearchCodeMatches` 跑 `rg --json`，解析结构化命中（历史 bug：rg JSON 有 `data` 包裹层）。
   - L2：`RunTreesitterQueryResults`——"TODO" 不是合法 CSP，**静默无贡献**（自然语言 query 不算错误）。
   - L3：`querySymbols`——clangd 对 "TODO" 无符号结果 → 触发 ripgrep 降级（带 WARNING）。
5. `wg.Wait()` 后**串行归一化**（文件读取走缓存，避免竞争）：
   - rg 命中 → snippet 原子，`snippetExpander` 按行号 ±2 行扩展（router.go:618-666），带每文件行偏移表缓存。
   - 降级命中 → `SourceTool="rg(lsp-fallback)"` 的 snippet 原子。
6. 管道：`MergePhysical`（相邻 TODO 窗口合并）→ `DedupSemantic`（rg 与降级重复命中合并）→ `CropBudget(8KB)`。
7. `Render`：统计头 + 按文件分组 + WARNING + 窄化提示。
8. 结果连同**文件依赖集**（含被预算丢弃的原子所在文件，router.go:173-190 的 `resultFiles`）写入缓存。

实测：5569 原始命中 → 1459 原子 → 42 条 L0 展示，输出 7.9KB。

### 3.2 `search(query="device_probe", intent="definition")` —— intent 路由到符号层

1. `routeByIntent` 命中 "definition" → `searchLayerUnified(layer="symbol")`（router.go:368）。
2. `querySymbols` → clangd `workspace/symbol` 返回符号列表。
3. `atomsFromSymbols`（router.go:719-752）：每个符号成原子（`SemanticID="name@path"`，Priority 3）；**前 5 个**（`maxSymbolDefinitions`，延迟护栏）额外调 `GetFullDefinition` 抓完整实现体作 L0 载荷。
4. 管道后 Render。实测：6 原子、5 full / 1 signature、7.7KB/8KB——高价值符号全给实现体，第 6 个给签名。

*冷启动注意*：clangd 的索引在 initialize 后还要装载几秒（仓里用 `warmUpLSP` 打开 compile_commands.json 首个 TU 触发后台索引，main.go:115-139）。此窗口内符号层查询返回空 → 按 D3 哲学降级为文本搜索 + WARNING。这不是 bug，是设计的降级语义。

### 3.3 `definition(symbolName="device_probe")` —— 确定性导航

1. tools.go handler → `tools.ReadDefinition`（definition.go）。
2. `ResolveSymbolLocation`（symbol_resolver.go）：`workspace/symbol` 查询 → 保守匹配（精确名优先，`a.b.C` 形式按最后一段匹配，filePath 作消歧提示）→ 得 file:line:col。
3. `OpenFile`（幂等）+ `GetFullDefinition`（lsp-utilities.go:20）：`DocumentSymbol` 拿到文档符号树，递归找**包含该位置的最内层符号**，取其 range；再从磁盘读文件按行截取——处理 Python 那种"符号 range 只到开括号"的边角（lsp-utilities.go:89-125 的括号配平）。
4. 实测返回 137 行完整函数体，带 `Range: L485:C1 - L621:C2`。

### 3.4 `callers/callees` —— 调用链

`call_hierarchy.go`：`PrepareCallHierarchy` 定位 → `IncomingCalls`/`OutgoingCalls` **递归**收集，`seen` 集合（`uri:line:col`）防环。depth 默认 1、clamp 3——百万行仓热函数 depth 3 的扇出是爆炸性的。输出按 depth 分组，16KB 截断 + 省略计数。同时返回 `structuredContent` + fallbackText（tools.go 的 `newAppToolResult`），带 `Meta.ui.resourceUri` 指向 MCP App 资源（`ui://call-hierarchy/graph`），支持 UI 的客户端渲染成图，纯文本客户端看文本。

### 3.5 文件变更的同步旅程

1. fsnotify 事件 → watcher 事件循环（watcher.go:213）。
2. 排除检查（目录/文件规则 + gitignore + 5MB 上限）→ `isPathWatched`（LSP 注册的 glob 匹配；无注册则全 watch）。
3. 300ms 防抖（per-uri+type 的 `time.AfterFunc`，watcher.go:509-530）。
4. `handleFileEvent`：
   - 已打开文件的 Changed → `NotifyChange` → **增量 diff**（D5）；
   - 其他 → `didChangeWatchedFiles` 通知 LSP；
   - 同时回调 `OnFileChange(uri)` → `Router.InvalidateFile`（缓存按文件失效）→ 若改的是 `compile_commands.json` 再 `InvalidateIncludeMap`（main.go:155-160）。

---

## 4. 关键算法逐个讲

### 4.1 MergePhysical：扫描线合并 + 父子折叠（atom.go:103-168）

同一文件内按 StartByte 排序（同起点：大范围优先，再 Priority 优先），维护 `maxEnd` 扫描线：

- **包含关系**（子原子 EndByte ≤ maxEnd）：若容器是 FUNCTION/STRUCT 这类"可折叠块"→ **父子折叠**——父子都保留，但容器 `MaxLevel` 降为 L1，预算器最多给它骨架（signature），全文额度留给子级；否则直接吞并子级。
- **部分交错**（只可能来自异构源，比如 rg 窗口跨进 AST 节点）：按 Priority 取舍，不重解析做 LCA 合并（backlog 项）。
- `StartByte < 0`（坐标不明，如 workspace/symbol 只有行号且文件读不到）：豁免吞并，防止误杀。

### 4.2 DedupSemantic：语义去重（atom.go:172-187）

按 SemanticID 哈希去重，保留最高 Priority。三类 ID 策略：symbol 用 `name@path`（LSP 不暴露 USR，这是 Phase 1 的 FQN 近似）；AST 节点用 `path:nodetype:offset`；snippet 用 `path:offset`。效果：rg 命中和 LSP 降级命中同一行时合并为一个原子，clangd 版本胜出（Priority 3）。

### 4.3 CropBudget：四相降级背包（atom.go:211-258）

按 Priority 降序遍历，每个原子从 `MaxLevel` 起依次尝试 L0→L1→L2，第一个装得下的级别成交；都装不下才丢弃。**计费 = 载荷 + 渲染开销**（per-atom tag 24B + 首次出现的文件头 path+10B）——预算是"诚实"的，不会出现账面 8KB 实际输出 15KB。

### 4.4 snippetExpander（router.go:618-666）

rg 命中是单行，作为 snippet 原子的 L0 载荷太单薄。按命中行 ±2 行扩展，相邻窗口经 MergePhysical 自动合并成连续块。每文件一份内容 + 行起始偏移表缓存（一次查询内 rg 命中常密集于同文件）。

### 4.5 IncludeMap（includemap.go）

解析 compile_commands.json 为双向映射（TU ↔ -I/-isystem/-iquote 目录）。`Neighborhood(file)` = 与锚文件共享至少一个**有区分度** include 目录的文件集。**无区分度剔除**：被全仓所有 TU 使用的目录（u-boot 的 `include/`）不参与——否则全仓互为邻居，限定失去意义（includemap.go:117-121）。典型价值：宏控多板仓里 clangd 只索引了当前 defconfig，降级搜索限定到"同编译族"而不是全仓。实测：锚定 `drivers/core/device.c` 时邻域大到触发 400 文件上限（router.go 的 `maxScopeFiles`）。

### 4.6 增量 diff + UTF-16 位置（client.go:470-546）

`computeIncrementalChanges(old, new)`：公共前缀/后缀裁剪得到单个变更区间；边界回退到 UTF-8 rune 起点（防止把多字节字符劈开）；区间为空则全量。`positionAt` 把字节偏移换算成 LSP 位置——行号 0 起、列按 **UTF-16 code unit** 计（emoji 算 2，LSP 默认编码；本 client 不协商 `positionEncodings`）。测试用"应用变更回环验证"：用算出的 range 把 old 改成 new 必须逐字节成立（client_sync_test.go）。

### 4.7 缓存：TTL + 反向索引（cache/cache.go）

- 键含 intent，杜绝不同 intent 串扰；
- 值带**文件依赖集**（含被预算丢弃的原子的文件——这些文件变了结果也可能变）；
- `byFile` 反向索引让 `DeleteByFile` 是 O（依赖该文件的条目数） 而不是全表扫描；无依赖信息的条目保守失效；
- 写入时顺带清扫过期条目（修复前 `Cleanup()` 无人调用，map 只增不减）。

---

## 5. 并发模型

goroutine 拓扑（进程存活期间）：

```
main goroutine ── server.ServeStdio（MCP 读循环，每请求一个 handler goroutine）
├── handleMessages（LSP stdout 读循环，唯一读源）
├── stderr 转发 goroutine
├── watcher.WatchWorkspace（fsnotify 事件循环）
│     └── 每事件 time.AfterFunc 防抖回调（短期 goroutine）
├── 父进程监控 ticker（100ms，ppid 变化即自杀——Claude Desktop 不杀子进程）
└── searchAll 时：3 个层 goroutine（wg.Wait 汇合，归一化串行）
```

锁清单（谁保护什么）：

| 锁 | 位置 | 保护 |
|----|------|------|
| `writeMu` | lsp.Client | stdin 的 header+body 原子写 |
| `handlersMu` | lsp.Client | 请求 ID → 响应 channel 表 |
| `openFilesMu` | lsp.Client | 打开文件表（版本号/内容快照） |
| `diagnosticsMu` / `diagWaitersMu` | lsp.Client | 诊断缓存 / 诊断等待者 |
| `mu` | cache.Cache | 条目 + byFile 反向索引 + unknownDeps |
| `includeMu` | router.Router | include map 懒加载/失效 |
| `mu` | router.searchAll 局部 | 三路结果收集 |

LSP 协议层的一个隐式约束：**Call 是并发安全的**（每请求独立 channel 按 ID 分发），但 NotifyChange 与 watcher 防抖天然串行化了对同一文件的 didChange 频率。

## 6. 生命周期

**启动序列**（main.go）：

```
parseConfig → newServer → start:
  initializeLSP: lsp.NewClient（起子进程）→ initialize 握手（声明能力）
    → 注册服务端请求/通知 handler → TypeScript 特判初始化
    → 启动 watcher goroutine → WaitForServerReady（workspace/symbol 可应答，30×500ms）
  warmUpLSP: 打开 compile_commands.json 首个 TU，触发 clangd 后台索引
  router + watcher.OnFileChange 回调接线
  registerTools（条件注册）→ ServeStdio
```

**关闭序列**：SIGINT/SIGTERM 或父进程死亡 → CloseAllFiles → shutdown（500ms 超时，挂了就放弃）→ exit 通知 → Close（2 秒不退就 SIGKILL）。

**LSP 死亡路径**（运行中）：读循环 EOF/出错 → `markDead` + 所有挂起 Call 立即收到 -32001 错误 → 后续 Call/Notify 快速失败 → router 符号层走降级，WARNING 写明 "process exited"。没有自动重启（已知边界，见 §8）。

## 7. 模块速查表

| 文件 | 职责 | 关键函数/类型 | 测试 |
|------|------|--------------|------|
| main.go | 入口、编排、warmup、关闭 | `start` `warmUpLSP` `cleanup` | — |
| daemon.go | daemon 部署：HTTP 传输、会话文件、idle 回收 | `runDaemon` `sessionLive` | daemon_test.go |
| proxy.go | stdio↔daemon 桥接、daemon 自动拉起 | `runProxy` `ensureDaemon` `mirrorTools` | （e2e 冒烟验证） |
| tools.go | 工具注册（条件）+ 参数解析 | `registerTools` `formatSearchResults` | — |
| internal/tools/router/router.go | 路由、三层扇出、归一化生产 | `Search` `searchAll` `searchLayerUnified` | router_test.go, router_mock_test.go |
| internal/tools/atom/atom.go | IR + 三算法 | `MergePhysical` `DedupSemantic` `CropBudget` | atom_test.go |
| internal/tools/ripgrep.go | rg JSON 解析 | `SearchCode` `SearchCodeMatches` | ripgrep_test.go |
| internal/tools/treesitter/*.go | C/C++ AST 查询 | `QueryDirectory`（worker pool） | query_test.go |
| internal/tools/includemap.go | 编译族邻域 | `Neighborhood` | includemap_test.go |
| internal/tools/cache/cache.go | TTL 缓存 + 反向索引 | `DeleteByFile` | cache_test.go |
| internal/lsp/client.go | 进程管理、文件同步、诊断 | `OpenFile` `NotifyChange` | client_sync_test.go |
| internal/lsp/transport.go | 消息编解码、分发 | `Call` `Notify` `handleMessages` | （经 client 测试覆盖） |
| internal/lsp/methods.go | ~60 个 LSP 方法封装 | `Symbol` `PrepareCallHierarchy` | — |
| internal/tools/definition.go 等 | 单点工具实现 | `ReadDefinition` `GetCallersData` | call_hierarchy_test.go 等 |
| internal/watcher/watcher.go | fsnotify + 防抖 + 注册匹配 | `WatchWorkspace` `handleFileEvent` | watcher/testing（基线有时序失败） |
| internal/protocol/ | LSP 类型（gopls codegen 生成） | `tsprotocol.go` 7k 行 | — |
| cmd/generate/ | 协议代码生成器 | — | — |
| ui_resources.go | MCP App HTML 资源 | 调用图/诊断面板 | — |

## 8. 已知边界与未落地项

- **单 workspace + 单 LSP 实例**：多进程客户端的"N 份 LSP 导致 OOM"已由 daemon+proxy 部署模式解决（daemon 独占 LSP，proxy 做 stdio 桥接，见《docs/daemon-proxy-design.md》）；多语言混合仓仍需按语言各跑一个 daemon。`os.Chdir(workspaceDir)` 使单进程只能服务一个仓。
- **tree-sitter 仅 C/C++**：Go/Rust/Python 仓的 ast 层静默无贡献（`search` 的 language 默认 "cpp" 暴露了 C/C++ 优先偏向）。
- **USR 全局符号身份未落地**（backlog #9）：LSP 不暴露，需读 clangd 索引私有格式；当前用 `name@path` 近似，同名符号跨文件会串。
- **LSP 进程不自动重启**：死亡后只能降级运行，需重启 MCP server 恢复符号层。
- **watcher 三个测试时序性失败**（环境基线问题，非代码回归）；go/hover、go/diagnostics、clangd 集成快照漂移（文档 P1-4，待固定工具版本后重生成）。
- **打开文件只开不关**：长会话内存随检视文件数增长（退出时才 CloseAllFiles），无 LRU 上限。
- **stdin EOF 不退出**：stdin 关闭后进程仍等 `<-done`（真实用法中由父进程监控兜底）。

## 9. 建议的源码阅读路径

1. 先读 `docs/code-atom-ir.md` 的 §1-§4（管道的设计文档），再读 `internal/tools/atom/atom.go`（约 310 行，一小时能吃透）——这是全仓的心脏。
2. 顺着 `router.go` 的 `Search` → `searchAll`/`searchLayerUnified` 看三个 `atomsFromXxx` 生产者，理解异构结果怎么进管道。
3. 读 `internal/lsp/transport.go`（274 行）理解消息分发，再读 `client.go` 的 OpenFile/NotifyChange。
4. 用 §3 的流程追踪对照 `tools.go` 的某个 handler 走一遍完整链路。
5. 最后看 watcher 和 cache 这两个横切系统。

---

*本文与代码同步于 2026-07-20（写锁/增量同步/缓存反向索引/条件注册/只读工具面落地之后）。*
