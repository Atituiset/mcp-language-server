# MCP 工具面优化方案

> 重写版，2026-07-17，基于 commit `cc756da`（main）。
>
> **对旧版的修正**：旧文档假设"14 个工具、需新建统一搜索入口"，与代码现状不符——
> 实际注册 **17 个工具**，且统一搜索入口（`search` 工具 + `internal/tools/router`）**已经存在**。
> 问题不在架构缺失，而在路由实现质量与工具面过宽。本文以代码现状为准重新盘点，并给出收敛方案。
>
> 实施任务见《docs/handoff-tasks.md》任务 B（B1–B8 与本文方案的对应关系见 §7）。

---

## 1. 现状盘点

### 1.1 工具清单（17 个，`tools.go` `registerTools()`）

| # | 工具 | 参数面（* = 必填） | 层级 | 备注 |
|---|------|------------------|------|------|
| 1 | `search` | `query`*、`strategy`(auto/text/ast/symbol，默认 auto)、`intent`、`filePath`、`language` | L1/L2/L3 | 统一入口，背后为 router |
| 2 | `search_text` | `query`*、`filePath` | L1 | 等价 `search strategy=text` |
| 3 | `search_ast` | `query`*、`filePath`、`language` | L2 | 等价 `search strategy=ast` |
| 4 | `search_symbol` | `query`*、`filePath` | L3 | 等价 `search strategy=symbol` |
| 5 | `ripgrep` | `pattern`*、`caseSensitive`、`wholeWord`、`maxCount`(默认100)、`contextLines`、`fileType`、`include` | L1 | 全参数文本搜索 |
| 6 | `treesitter_query` | `query`*（CSP 模式）、`filePath`、`language` | L2 | 仅 C/C++ |
| 7 | `treesitter_ast` | `filePath`*、`nodeType`、`maxDepth`(默认10) | L2 | 查看 AST 结构 |
| 8 | `find_struct_usage` | `structName`*、`filePath`、`language`(默认 cpp) | L2 | |
| 9 | `find_struct_definition` | `structName`*、`filePath`、`language`(默认 cpp) | L2 | |
| 10 | `definition` | `symbolName`* | L3 | 符号完整定义源码 |
| 11 | `references` | `symbolName`* | L3 | 符号全部引用位置 |
| 12 | `callers` | `symbolName` 或 `filePath`+`line`+`column`；`depth`(默认1，clamp 10) | L3 | UI Meta `ui://call-hierarchy/graph` |
| 13 | `callees` | 同 `callers` | L3 | 同上 |
| 14 | `diagnostics` | `filePath`*、`contextLines`(默认5)、`showLineNumbers`(默认 true) | L3 | UI Meta `ui://diagnostics/dashboard` |
| 15 | `hover` | `filePath`*、`line`*、`column`* | L3 | |
| 16 | `rename_symbol` | `filePath`*、`line`*、`column`*、`newName`* | L3 | |
| 17 | `edit_file` | `filePath`*、`edits`*[{`startLine`*,`endLine`*,`newText`}] | — | |

注：`get_codelens`/`execute_codelens` 已在代码中注释（`tools.go:198-261`），不计入。

### 1.2 统一搜索路由架构（已存在，勿重建）

```
search 工具 (tools.go:524)
  └─ router.Router.Search (router.go:60)
       ├─ 缓存查找：SearchCacheKey(query, strategy, filePath, language)  (router.go:66)
       ├─ strategy=text   → searchText   (router.go:112)  ripgrep，MaxCount 硬编码 100
       ├─ strategy=ast    → searchAST    (router.go:125)  tree-sitter CSP
       ├─ strategy=symbol → searchSymbol (router.go:140)  LSP workspace/symbol，失败静默降级 ripgrep
       └─ strategy=auto   → searchAuto   (router.go:167)
            ├─ intent 非空 → routeByIntent (router.go:219) 关键词命中 → 单层
            └─ 否则        → searchAll    (router.go:183) 三层并行，全量拼接
```

- **缓存**：内存缓存，TTL 5 分钟（`router.go:36` `defaultCacheTTL = 300`；`NewRouterWithCache` 支持自定义 TTL 但 main.go 未使用）。文件变更时 `main.go:114-116` 通过 `watcher.OnFileChange` **全量清空**。
- **输出**：`tools.go:900` `formatSearchResults` 把各层 `SearchResult.Content` 顺序拼接，无截断。

---

## 2. 问题清单（按优先级，含行号，可复现）

| # | 位置 | 问题 | 影响 | 复现 |
|---|------|------|------|------|
| P0-1 | `router.go:66` | 缓存键 `SearchCacheKey(query, strategy, filePath, language)` **不含 intent**。auto 策略下同 query 不同 intent 互相污染 | 错误结果 | 依次调用 `search(query=X, intent="todo")` 与 `search(query=X, intent="function")`，第二次直接命中第一次的缓存，返回错误层结果 |
| P0-2 | `router.go:183-217` + `tools.go:900-913` | `searchAll` 三层并行**全量拼接**，无任何截断 | 上下文爆炸 | 大仓上 `search(query="struct")`（不传 intent）→ 三层结果拼接可达数万行 |
| P0-3 | `router.go:140-165` | `searchSymbol` 在 LSP 失败/空结果时**静默降级 ripgrep**，仍标注 `Layer: "symbol"` | 误导 LLM 把文本命中当语义结果 | LSP 未就绪时调用 `search_symbol`，返回内容为 ripgrep 文本但层标为 symbol |
| P1-1 | `tools.go:730-732`、`tools.go:801-803` | callers/callees `depth` clamp 为 10。百万行仓调用图上组合爆炸 | 上下文爆炸、LSP 慢 | 对热点函数 `callers(depth=10)`，输出体积与耗时失控 |
| P1-2 | `router.go:112-123` | `searchText` 硬编码 `RipgrepOptions{MaxCount: 100}`，丢弃 caseSensitive/contextLines/fileType 等参数 | 能力损失 | `search strategy=text` 无法传 contextLines，只能改用 `ripgrep` 工具 |
| P1-3 | `tools.go` 整体 | 17 个工具全量注册，其中 8 个（search_text/search_ast/search_symbol/ripgrep/treesitter_query/treesitter_ast/find_struct_usage/find_struct_definition）与 `search` 功能重叠 | tool confusion、schema 膨胀 | 观察 MCP `tools/list` 返回 |
| P2-1 | `router.go:219-245` | `routeByIntent` 仅英文关键词，中文 intent 不命中 | 路由失效 | `search(intent="找定义")` → 落入 searchAll |
| P2-2 | `main.go:114-116` | 任意文件变更**全量清空**搜索缓存 | 大仓上缓存命中率趋零 | 保存任一文件后重复同一 search，全部重新计算 |
| P2-3 | `main.go:113` | 使用 `NewRouterWithClient`，TTL 硬编码 300s，`NewRouterWithCache` 的自定义 TTL 通路未被使用 | 不可调 | 读代码即得 |

---

## 3. 对外收敛方案：默认 9 工具 + 8 debug 工具

### 3.1 默认暴露（9 个）

`search`（声明为默认首选入口）、`definition`、`references`、`callers`、`callees`、`diagnostics`、`hover`、`edit_file`、`rename_symbol`

### 3.2 降级为 debug 工具（8 个）

仅当环境变量 **`MCP_LS_DEBUG_TOOLS=1`** 存在时注册：

`search_text`、`search_ast`、`search_symbol`、`ripgrep`、`treesitter_query`、`treesitter_ast`、`find_struct_usage`、`find_struct_definition`

### 3.3 取舍理由

- `search_text/search_ast/search_symbol`：与 `search` + `strategy` 完全等价，纯重复入口，是 tool confusion 的主要来源。
- `ripgrep`：能力是全参数文本搜索，但 P1-2 修复后 `search strategy=text` 可透传主要参数；全参数版保留为 debug 逃生舱（需要 fileType/include 等高级过滤时）。
- `treesitter_query/treesitter_ast/find_struct_*`：均已被 `search strategy=ast` 或 `definition`/`references` 覆盖；调试 AST 查询本身时才需要直连。
- 保留的 9 个工具语义两两互斥（见 §6 description），LLM 的选择题从 17 选 1 降为 9 选 1，且 80% 场景应由 `search` 承接。

---

## 4. 参数与输出硬限制

### 4.1 callers/callees depth clamp 10 → 3

`tools.go:730-732`、`tools.go:801-803`：`if depth > 3 { depth = 3 }`，description 同步改为 "max: 3"。
理由：深度每 +1，结果数按调用图扇出倍数增长；depth=3 已覆盖"直接调用方 → 二级调用方 → 三级入口"的审计视角，更大深度应通过多次调用迭代而非一次爆炸。

### 4.2 search 输出截断

在 router 层与格式化层各设一道闸：

| 闸 | 位置 | 规则 |
|----|------|------|
| 单层截断 | router 各 searchXxx 返回前 | 每层最多 **50 行或 4KB**（先到为准） |
| 总量截断 | `formatSearchResults`（`tools.go:900`） | searchAll 拼接后总长 ≤ **12KB** |

截断部分统一标注：

```
... [truncated, N more lines, use strategy=text with filePath to narrow]
```

（strategy 提示按被截断的层动态替换为 text/ast/symbol。）

设计思想引用《docs/code-atom-ir.md》：**降级而非丢弃**——截断标注必须告诉 LLM 还有多少内容、用什么参数能窄化拿到，而不是静默砍断。

---

## 5. 路由修复方案

### 5.1 缓存键加入 intent（修 P0-1）

- `cache.go:116` `SearchCacheKey` 签名加 `intent` 参数：`SearchCacheKey(query, strategy, filePath, language, intent string)`。
- 唯一调用方为 `router.go:66`，传入 `opts.Intent`。
- 显式层策略（text/ast/symbol）不受 intent 影响，但 intent 参与键计算无害（同一显式调用 intent 恒定）。

### 5.2 searchSymbol 降级标注（修 P0-3）

`router.go:140-165` 降级分支：

- `Layer` 改为 `"symbol-fallback-text"`；
- 内容头部加一行：`WARNING: LSP unavailable, results are plain text matches`。

LLM 看到该标记即知道结果无语义保证，必要时改用 `strategy=text` 显式重查。

### 5.3 routeByIntent 中文关键词（修 P2-1）

`router.go:219-245` 关键词表补充：

| 目标层 | 新增中文关键词 |
|--------|---------------|
| text | 注释、文本、待办 |
| ast | 函数、结构体、语法 |
| symbol | 定义、引用、调用、声明 |

匹配逻辑不变（`strings.Contains`），中英文混排 intent 自然命中。

---

## 6. 工具 description 重写（触发式）

原则：每条 description 回答"什么时候用我 / 什么时候别用我"，并与 §3 的互斥分工一致。以下为 9 个保留工具的 description 全文（英文，与现有代码风格一致）：

- **search**
  `Primary search entry — use this FIRST whenever you need to find code, symbols, or text in the workspace. strategy='auto' (default) routes intelligently across text/AST/symbol layers; set 'text'/'ast'/'symbol' only to force a layer. Use filePath to narrow scope. Do NOT use this to read a known symbol's full implementation — use definition instead.`
- **definition**
  `Use when you already know a symbol's name and need its complete implementation source code. Do NOT use for exploratory or fuzzy search — use search first.`
- **references**
  `Use when you know a symbol's name and need every location that references it across the codebase. Do NOT use for keyword or pattern search — use search.`
- **callers**
  `Use when you need to know who calls a function (incoming call hierarchy), e.g. tracing untrusted data sources upward. Prefer filePath+line+column for precision. depth defaults to 1 and is clamped to 3 — do not request larger depths; iterate with follow-up calls instead.`
- **callees**
  `Use when you need to know what a function calls (outgoing call hierarchy), e.g. tracing sinks downward. Same parameters as callers; depth defaults to 1 and is clamped to 3.`
- **diagnostics**
  `Use after editing a file to check compiler errors and warnings reported by the language server. Requires an exact filePath; do NOT use for files you have not opened or edited.`
- **hover**
  `Use when you need the type signature or documentation of the symbol at a known file:line:column position. Do NOT use when you only know a name — use definition or search.`
- **edit_file**
  `Use to apply precise line-range text replacements in a file. startLine/endLine are 1-indexed and inclusive; leave newText empty to delete lines.`
- **rename_symbol**
  `Use to rename a symbol at a known file:line:column position and update all references codebase-wide. Requires the exact position of the symbol.`

---

## 7. 落地清单

对应《docs/handoff-tasks.md》任务 B 编号。

### P0（正确性与止血）

| 项 | 修改 | 文件 |
|----|------|------|
| B1 | 缓存键加 intent（§5.1） | `internal/tools/cache/cache.go:116` + `internal/tools/router/router.go:66` |
| B2 | searchAll 截断（§4.2） | `router.go:183-217` + `tools.go:900-913` |
| B3 | searchSymbol 降级标注（§5.2） | `router.go:140-165` |
| B4 | 工具收敛 17 → 9+8（§3） | `tools.go` `registerTools()`，`os.Getenv("MCP_LS_DEBUG_TOOLS")` |

### P1（体验与防呆）

| 项 | 修改 | 文件 |
|----|------|------|
| B5 | depth clamp 10→3（§4.1） | `tools.go:730-732`、`801-803` |
| B6 | description 重写（§6） | `tools.go` 各 `mcp.NewTool(...)` |
| B7 | 中文 intent 关键词（§5.3） | `router.go:219-245` |

### P2（能力补全）

| 项 | 修改 | 文件 |
|----|------|------|
| B8 | searchText 参数透传：`SearchOptions` 增加 maxCount/contextLines/caseSensitive 等并透传到 `RipgrepOptions`（§2 P1-2） | `router.go:112-123` + `SearchOptions` |
| B9 | 缓存 TTL 可调：main.go 改用 `NewRouterWithCache` 通路或给 `NewRouterWithClient` 加 TTL 参数，经环境变量/flag 暴露（§2 P2-3） | `main.go:113`、`router.go:45-58` |
| B10 | 文件变更缓存失效精细化：全量 Clear → 按文件维度失效（§2 P2-2）。大仓上避免一次保存清空全部缓存 | `main.go:114-116`、`internal/tools/cache/cache.go` |

---

## 8. 验证方法

```bash
cd ~/Projects/mcp-language-server
go build ./...                          # 编译
go test ./internal/...                  # 单元测试
go test ./integrationtests/...          # 集成测试（需 gopls/clangd 等）
UPDATE_SNAPSHOTS=true go test ./integrationtests/...  # 更新快照（B4 工具面变更后必须）
```

注意：B4 会改变 MCP `tools/list` 返回，**必须更新集成测试快照**并人工核对 diff 只剩工具列表变化。

手动验证点：

1. **P0-1 复现消失**：同 query 不同 intent 连续调用，结果各自正确、不再串缓存。
2. **P0-2**：大仓 `search`（无 intent）返回 ≤ 12KB 且尾部带 truncated 标注。
3. **P0-3**：停掉 LSP 后 `search_symbol`，Layer 显示 `symbol-fallback-text` 且带 WARNING 头。
4. **B4**：默认 `tools/list` 为 9 个；`MCP_LS_DEBUG_TOOLS=1` 重启后为 17 个。
5. **B5**：`callers(depth=10)` 实际按 depth=3 返回。
