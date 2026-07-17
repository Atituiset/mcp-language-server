# 优化点归档（Backlog）

> 归档日期：2026-07-17。来源：工具面收敛（`e9cf03b`、`6c21701`）、基线压测（`35b3431`、`b4d886f`、`4d274fb`）、CodeAtom IR Phase 1（`282e7b9`）。
> 每项含：问题/动机（含证据）、落点、备注。按建议优先级分组。

---

## P1 正确性与一致性（建议先做）

### 1. text 层忽略 filePath 参数
- **问题**：`search` 工具声明了 `filePath` 窄化范围，但 `router.searchText` 调 `tools.SearchCode` 时始终全仓搜索（`internal/tools/router/router.go`）。截断提示语还让 LLM "use strategy=text with filePath to narrow"——实际无效。
- **修法**：`RipgrepOptions` 增加路径限定（rg 支持以文件/目录为目标），或复用 `Include` glob。同步改 `SearchCodeMatches`（unified 管道同样受影响）。

### 2. intent 路由路径接入归一化
- **问题**：auto+intent 命中后走单层原样输出（仅 50 行/4KB 截断），不享受 Phase 1 的 merge/dedup/budget。同一 `search` 入口，两种输出语义不一致。
- **落点**：`router.searchAuto` → 单层结果也过 atom 管道（单层内同样可能有 symbol+text 混合，如 fallback）。

### 3. `docs/MCP_TOOLS_ZH.md` 与默认 9 工具脱节
- **问题**：该文档按 17 工具全量编写（debug 工具名出现 27 处），未反映 B4 收敛与 `MCP_LS_DEBUG_TOOLS` 机制。
- **修法**：标注默认暴露的 9 个 + debug 8 个的启用方式；description 文案与新触发式对齐。

### 4. 集成测试快照环境漂移
- **问题**：`go/hover`、`go/diagnostics`、clangd 全套在干净 `cc756da` 上即失败——gopls/clangd-18 输出格式与快照漂移，与代码改动无关（已用干净 worktree 双向验证）。watcher 三个测试在本环境失败（debounce/fsnotify 时序）。
- **决策点**：固定 CI 工具版本后 `UPDATE_SNAPSHOTS=true` 重生成（推荐），或放宽快照比对。**不要**在当前漂移环境直接重生成。

---

## P2 能力增强（CodeAtom IR Phase 2 方向）

### 5. 父子结构折叠（code-atom-ir §3.3）
- **内容**：父级容器（大函数/struct/class）与子命中同时存在时，父级降为 Signature 骨架、子级保留 L0，省 ~90% 无关实现代码。
- **前置**：需要原子的层级关系（tree-sitter 原生可得）+ definition 载荷。

### 6. symbol 原子 L0 载荷
- **内容**：对 Top-N 高优先 symbol 原子按需发 `textDocument/definition` 抓实现体（限制 N 控延迟），当前 symbol 原子只有 L1/L2。

### 7. rg 命中 ±2 行上下文扩展
- **内容**：snippet 读文件按行扩展，提高文本命中可读性（文档 §1 原始设计）。

### 8. 交错块 LCA 合并
- **内容**：重叠（非包含）AST 块用 tree-sitter 找最近公共祖先合并，替代 Phase 1 的直接吞并。

### 9. USR 全局符号身份
- **内容**：`SemanticID` 从 `name@path` 升级为 clangd USR，实现跨文件真去重。
- **难点**：LSP 协议不暴露 USR；只能读 clangd 索引分片（`.cache/clangd/index`，格式非稳定 ABI）或换 C++ 侧通道，成本高。

### 10. B10 缓存按文件失效
- **现状**：任意文件保存 → 全量 Clear（防抖 300ms 兜底）。atom 化后可行：缓存条目记录涉及的文件集合，失效时按文件反查。

### 11. QueryDirectory 全仓 AST 解析性能
- **问题**：ast 层在 u-boot 上遍历解析 13k+ 文件，无并发无缓存。自然语言 query 还会全部报错浪费一次全仓遍历。
- **修法**：并发解析 worker pool + 按 mtime 的结果缓存；或在 query 明显非 CSP 模式时跳过 ast 层。

### 12. callers 压测对象修正
- **问题**：srsRAN 基线选的 `handle_rrc_setup_request` 仅 1 个调用者（统一 dispatch 架构），压不出调用链体积。
- **修法**：换热点函数（srslog 系列、byte_buffer、span 等基础设施）重测。

---

## P3 压测仓扩展

### 13. 真实单板 defconfig 验证
- **内容**：u-boot 换 `stm32mp15_defconfig`/`qemu_arm64_defconfig` 出 compile_commands，验证宏分支下 LSP 准确性（当前 sandbox 只覆盖核心路径）。
- **前置**：需 ARM 交叉工具链（本机仅 host gcc；可用 apt 下载 `.deb` 本地解压的老办法，或用户授权安装）。

### 14. openairinterface5g 接入评估
- **内容**：宏密度最像电信 3GPP 设备。先验证能否生成可用 compile_commands.json（`build_oai` 构建较脏），再决定是否投入。

### 15. 极限仓（可选，慎入）
- **内容**：torvalds/linux 3000 万+ 行终极压测；clangd 索引极重，需先评估内存/索引时长，建议放在 Phase 2 稳定后。

---

## 已完成（备查，勿重复立项）

- 工具面收敛 17→9+debug、缓存键 intent、search 截断、降级标注、depth clamp、description 重写、中文 intent（`e9cf03b`）
- search 透传 ripgrep 参数、缓存 TTL 可调（`6c21701`）
- ripgrep JSON `data` 包裹层解析修复（`b4d886f`）
- LSP 启动 warmup（clangd didOpen 触发索引）、callers/callees 16KB 截断（`4d274fb`）
- CodeAtom IR Phase 1：归一化/吞并/去重/四相预算（`282e7b9`）
