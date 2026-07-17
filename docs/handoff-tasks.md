# 任务交接：工具面收敛优化 + 大型 C/C++ 测试仓选型

> 本文档用于跨 session 交接。新 session 打开后**先读本文件**，再按 §5 指引恢复上下文，按 §4 任务清单执行。
> 撰写日期：2026-07-17。分析基于 commit `cc756da`（main 分支）。

---

## 1. 背景与目标

**项目**：`mcp-language-server`（Go 1.24，MCP server），向 LLM 暴露代码上下文提取能力（定义/引用/调用链/AST/文本搜索），目标场景是**百万级 C/C++ 代码库的安全漏洞检视**（典型：电信 3GPP 级别复杂度、多单板编译宏控制的代码仓）。

**两条主线任务**：

- **主线 A**：工具面优化。当前 17 个 MCP 工具全量暴露给 LLM，存在 tool confusion、上下文爆炸、缓存串扰等问题。需先**重写 `docs/tools-opt.md`**（原文基于错误假设），再按新文档实施代码修改。
- **主线 B**：选型一个 100 万~千万行级、多编译宏控制的 C/C++ 开源仓作为压测主体。

---

## 2. 仓库现状盘点（分析结论，可信）

### 2.1 实际注册的工具（17 个，`tools.go`）

原文档 `docs/tools-opt.md` 假设"14 个工具"，**与实际不符**。真实清单：

| # | 工具 | 说明 | 层级 |
|---|------|------|------|
| 1 | `search` | 统一搜索入口（query/strategy/intent/filePath/language） | L1/L2/L3 |
| 2 | `search_text` | 强制 L1 文本搜索 | L1 |
| 3 | `search_ast` | 强制 L2 AST 查询 | L2 |
| 4 | `search_symbol` | 强制 L3 符号搜索 | L3 |
| 5 | `ripgrep` | 全参数文本搜索（caseSensitive/wholeWord/maxCount/contextLines/fileType/include） | L1 |
| 6 | `treesitter_query` | CSP 模式查询 AST（仅 C/C++） | L2 |
| 7 | `treesitter_ast` | 查看文件 AST 结构 | L2 |
| 8 | `find_struct_usage` | 结构体使用位置 | L2 |
| 9 | `find_struct_definition` | 结构体定义 | L2 |
| 10 | `definition` | 符号定义 | L3 |
| 11 | `references` | 符号引用 | L3 |
| 12 | `callers` | 上游调用链（symbolName 或 filePath+line+column；depth 默认 1，上限 10） | L3 |
| 13 | `callees` | 下游调用链（参数同 callers） | L3 |
| 14 | `diagnostics` | 文件诊断（带 UI Meta `ui://diagnostics/dashboard`） | L3 |
| 15 | `hover` | 悬停信息 | L3 |
| 16 | `rename_symbol` | 重命名符号 | L3 |
| 17 | `edit_file` | 多重文本编辑 | — |

注：`get_codelens`/`execute_codelens` 已在代码中注释掉。

### 2.2 统一搜索路由已存在（`internal/tools/router/router.go`）

`search` 工具背后是 `router.Router`：strategy ∈ {auto,text,ast,symbol}，auto 时按 intent 关键词路由，无 intent 时 `searchAll` 三层并行。结果带 5 分钟 TTL 内存缓存（`internal/tools/cache/cache.go`），文件变更时 `main.go:114-116` 通过 `watcher.OnFileChange` 全量清缓存。

**结论：原文档建议的"合并出统一搜索入口"已存在，问题不在架构缺失，而在路由实现质量 + 工具面过宽。**

---

## 3. 发现的代码级问题（按优先级）

| # | 位置 | 问题 | 影响 |
|---|------|------|------|
| P0-1 | `router.go:66` | 缓存键 `SearchCacheKey(query, strategy, filePath, language)` **不含 Intent**。auto 策略下同 query 不同 intent 互相污染缓存 | 错误结果 |
| P0-2 | `router.go:183-217` | `searchAll` 三层并行**全量拼接**输出，无任何截断。大仓中一次 auto 搜索可返回数万行 | 上下文爆炸 |
| P0-3 | `router.go:140-165` | `searchSymbol` 在 LSP 失败/空结果时**静默降级 ripgrep** 且仍标注 `Layer: "symbol"` | 误导 LLM 把文本命中当语义结果 |
| P1-1 | `tools.go`（callers/callees handler） | `depth` 上限 `if depth > 10 { depth = 10 }`。depth=10 在百万行仓调用图上组合爆炸 | 上下文爆炸、LSP 慢 |
| P1-2 | `router.go:112-123` | `searchText` 硬编码 `RipgrepOptions{MaxCount: 100}`，丢弃 caseSensitive/contextLines/fileType 等参数 | 能力损失 |
| P1-3 | `tools.go` 整体 | 17 个工具全量注册，其中 8 个（search_text/search_ast/search_symbol/ripgrep/treesitter_query/treesitter_ast/find_struct_usage/find_struct_definition）与 `search` 功能重叠 | tool confusion、schema 膨胀 |
| P2-1 | `router.go:219-245` | `routeByIntent` 仅英文关键词，中文 intent（如"找定义"）不命中，落入 searchAll | 路由失效 |

---

## 4. 任务清单

### 任务 A（P0）：重写 `docs/tools-opt.md`

原文基于错误假设（14 工具、无统一入口），需推倒重写。新文档必须包含：

1. **现状盘点**：§2.1 的 17 工具真实清单 + 各自参数面；§2.2 路由架构说明。
2. **问题清单**：§3 表格（含文件行号，可复现）。
3. **对外收敛方案**：默认只暴露 **9 个工具**——`search`（声明为默认首选入口）、`definition`、`references`、`callers`、`callees`、`diagnostics`、`hover`、`edit_file`、`rename_symbol`；其余 8 个改为 debug 工具，仅当环境变量（建议 `MCP_LS_DEBUG_TOOLS=1`）存在时注册。说明取舍理由：重叠能力已被 search+strategy 覆盖；ripgrep 全参数版留作 debug 逃生舱。
4. **参数硬限制**：callers/callees depth clamp 10→3；search 各层输出截断策略（建议每层最多 50 行或 4KB，searchAll 总计 ≤ 12KB，超出部分标注 `... [truncated, N more lines, use strategy=text with filePath to narrow]`）。
5. **路由修复方案**：缓存键加入 intent；searchSymbol 降级时 Layer 改为 `"symbol-fallback-text"` 并在内容头部加 `WARNING: LSP unavailable, results are plain text matches`；routeByIntent 增加中文关键词（定义/引用/调用/结构体/函数/注释）。
6. **description 重写**：全部改为触发式（"当需要 X 时使用本工具；不要用 Y 场景"），`search` 的 description 明确"优先使用本工具，strategy 默认 auto；仅在结果不精确时才用 definition/references"。给出每个保留工具的 description 全文。
7. **落地清单**：P0（缓存键、searchAll 截断、searchSymbol 降级标注、工具收敛）→ P1（depth 限制、description 重写、中文 intent）→ P2（searchText 参数透传、缓存 TTL 可调）。
8. **验证方法**：见 §5.2。

### 任务 B（P0/P1）：按新文档实施代码修改

修改点一览（先做 P0 四项）：

| 修改 | 文件 | 要点 |
|------|------|------|
| B1 缓存键加 intent | `internal/tools/cache/cache.go`（SearchCacheKey 签名）+ `router.go:66` 调用处 | 加 `opts.Intent` 参数；注意更新所有调用方 |
| B2 searchAll 截断 | `router.go:183-217` + `tools.go` 的 `formatSearchResults` | 每层结果截断 + 总量上限 + 截断提示 |
| B3 searchSymbol 降级标注 | `router.go:140-165` | Layer 改名 + WARNING 前缀 |
| B4 工具收敛 | `tools.go` `registerTools()` | `os.Getenv("MCP_LS_DEBUG_TOOLS")` 控制 8 个 debug 工具注册 |
| B5 depth 限制 | `tools.go` callers/callees handler | `if depth > 3 { depth = 3 }` |
| B6 description 重写 | `tools.go` 各 `mcp.NewTool(...)` | 触发式文案，search 声明默认入口 |
| B7 中文 intent | `router.go:219-245` | 关键词表加中文 |
| B8 searchText 参数透传 | `router.go:112-123` + `SearchOptions` | 透传 maxCount/contextLines 等（可选，P2） |

### 任务 C（P1）：大型 C/C++ 测试仓选型与接入

> 调研 agent 中途因 API 断连死亡，以下为已恢复的搜索方向 + 已知事实整理，选型结论可信但 LOC 数字建议 clone 后用 `tokei`/`cloc` 复核。

**需求画像**：100 万~千万行 C/C++；3GPP 级协议复杂度；**多单板/多配置编译宏控制**（#ifdef、Kconfig、CONFIG_*）；能生成 `compile_commands.json` 供 clangd 索引。

| 候选仓 | 语言/规模（估） | 宏控制机制 | 3GPP 相似度 | compile_commands.json | 评价 |
|--------|----------------|-----------|------------|----------------------|------|
| **u-boot/u-boot** | C，实测 **176 万行**（.c 119 万 + .h 53 万 + .S 3.4 万，13405 文件，master@2026-07 浅克隆） | **Kconfig + CONFIG_*，1600+ 单板 defconfig** | 中（非电信，但单板宏控制最典型） | `scripts/gen_compile_commands.py`（官方自带，已验证出 1246 条） | ⭐ 单板宏场景首选 |
| **srsran/srsRAN_Project** | 现代 C++，实测 **100 万行**（.cpp 92 万，4680 文件，tag release_25_10） | CMake 选项 + 少量宏 | **高（真 5G gNB CU/DU，O-RAN）** | `cmake -DCMAKE_EXPORT_COMPILE_COMMANDS=ON`（已验证出 1116 条） | ⭐ 3GPP 场景首选，构建干净 |
| **open5gs/open5gs** | C，~30 万行 | Meson 选项 | 高（5GC/EPC 核心网） | meson 自动生成 | 偏小，可作补充 |
| **OPENAIRINTERFACE/openairinterface5g** | C，~150 万+ 行 | 大量 Makefile/CMake 宏 | **最高（真 3GPP RAN 协议栈）** | 构建脚本 `build_oai`，较脏，生成 compile_commands 需额外处理 | 复杂度高但接入成本也高 |
| **torvalds/linux** | C，3000 万+ 行 | Kconfig 极致 | 中 | `scripts/clang-tools/gen_compile_commands.py` | 终极压测，clangd 索引极重，慎入 |
| zephyrproject-rtos/zephyr | C，~200 万行 | Kconfig + devicetree，数百板 | 中 | west build 可导出 | 备选 |
| apache/nuttx | C，~200 万行 | Kconfig，多板 | 中 | 需 bear 拦截 | 备选 |

> ⚠️ srsRAN 归档注意（2026-07 实测）：GitHub `srsran/srsRAN_Project` 默认分支已被清空（只剩归档 README，项目于 2025-12 迁移至 OCUDU/gitlab.com/ocudu/ocudu）。**必须按 tag 克隆**：`git clone --depth 1 --branch release_25_10 ...`。克隆到 `~/Projects/testbeds/` 已完成，依赖经 apt 下载 .deb 本地解压在 `~/Projects/testbeds/.toolchain/`（无需 root）。

**推荐组合（Top-3 落地）**：

1. **主压测仓：U-Boot** —— "多单板编译宏控制"的最强开源样本（与电信多单板宏控制场景结构同构）；官方脚本直接出 compile_commands.json；可用 `make stm32mp15_defconfig` / `make qemu_arm64_defconfig` 等不同 defconfig 验证宏分支下 LSP 准确性。
2. **3GPP 语义仓：srsRAN_Project** —— 真 5G 协议栈，CMake 构建干净，clangd 友好；验证协议栈场景（NAS/RRC/PDCP 层级调用链）。
3. **极限挑战（可选）：openairinterface5g** —— 最像电信 3GPP 设备的代码风格和宏密度，但构建脏，先验证能否生成可用 compile_commands.json 再投入。

**接入 checklist**：
- [ ] clone 到 `~/Projects/testbeds/`（勿放本仓内）
- [ ] `tokei` / `cloc` 复核真实 LOC 并回填上表
- [ ] 生成 `compile_commands.json`（U-Boot: 选定 defconfig 后 `make -j$(nproc)` + `python3 scripts/clang-tools/gen_compile_commands.py`；srsRAN: cmake 参数）
- [ ] 启动：`mcp-language-server --workspace <repo> --lsp clangd`
- [ ] 压测项：clangd 冷启动索引耗时；`search`（auto，无 intent）返回体积是否被 B2 截断；`callers` depth=2/3 输出体积；`find_struct_usage` 在宏分支代码上的命中；缓存键修复前后 intent 串扰复现/消失
- [ ] 记录基线数据到 `docs/benchmark-*.md`

---

## 5. 新 session 恢复上下文指引

### 5.1 必读文件（按序）

1. 本文档（`docs/handoff-tasks.md`）
2. `tools.go`（938 行，全部工具注册；重点看 `registerTools`、callers/callees handler、`formatSearchResults`）
3. `internal/tools/router/router.go`（264 行，全部问题集中地）
4. `internal/tools/cache/cache.go`（缓存键签名）
5. `internal/tools/call_hierarchy.go`（CallResult 结构、输出格式）
6. 旧 `docs/tools-opt.md`（重写完即覆盖）
7. `docs/code-atom-ir.md`（背景方案，只读）

### 5.2 验证命令

```bash
cd ~/Projects/mcp-language-server
go build ./...                          # 编译
go test ./internal/...                  # 单元测试
go test ./integrationtests/...          # 集成测试（需 gopls/clangd 等）
UPDATE_SNAPSHOTS=true go test ./integrationtests/...  # 更新快照（工具面变更后必须）
```

工具收敛（B4）会改变 MCP 工具列表，**必须更新集成测试快照**。

---

## 6. 注意事项

- 旧 `docs/tools-opt.md` 的核心错误：假设 14 工具、建议"新建统一搜索入口"（实际已存在 `search`+router）。重写时明确说明这一修正。
- `main.go:113-116` 的文件变更全量清缓存策略在百万行仓上可能过激进（一次保存清空全部搜索缓存），可作为 P2 优化点写入新文档。
- `docs/code-atom-ir.md` 是后续方向（SemanticID/USR、字节偏移去重、L0/L1/L2 降级载荷、token 预算裁剪），本批任务不涉及，但 B2 截断策略可引用其"降级而非丢弃"思想。
