# 优化点归档（Backlog）

> 归档日期：2026-07-17。来源：工具面收敛（`e9cf03b`、`6c21701`）、基线压测（`35b3431`、`b4d886f`、`4d274fb`）、CodeAtom IR Phase 1（`282e7b9`）。
> 每项含：问题/动机（含证据）、落点、备注。按建议优先级分组。

---

## P1 正确性与一致性（建议先做）

### 1. ~~text 层忽略 filePath 参数~~ ✅ 已完成（本 commit）
- 已修：text 层带锚点时按 include 邻域（可区分时）或文件本身限定搜索（`textRipgrepOptions`），unified 管道的 text 生产者同步生效；输出带 `NOTE: scoped to N file(s) via include map`。

### 2. ~~intent 路由路径接入归一化~~ ✅ 已完成
- 已修：auto+intent 改走 `searchLayerUnified`（单层产出也过 atom 归一化/去重/预算管道，层标 `unified-text|ast|symbol`）；显式 `strategy=` 保留原始单层格式作为逃生舱。

### 3. ~~`docs/MCP_TOOLS_ZH.md` 与默认 9 工具脱节~~ ✅ 已完成
- 已修：补默认 9 工具/MCP_LS_DEBUG_TOOLS 说明、debug 工具标注、search 新参数与 unified 输出示例、depth max 3、diagnostics contextLines 类型修正、路由规则（含中文 intent）、缓存/索引行为说明。

### 4. 集成测试快照环境漂移
- **问题**：`go/hover`、`go/diagnostics`、clangd 全套在干净 `cc756da` 上即失败——gopls/clangd-18 输出格式与快照漂移，与代码改动无关（已用干净 worktree 双向验证）。watcher 三个测试在本环境失败（debounce/fsnotify 时序）。
- **决策点**：固定 CI 工具版本后 `UPDATE_SNAPSHOTS=true` 重生成（推荐），或放宽快照比对。**不要**在当前漂移环境直接重生成。

---

## P2 能力增强（CodeAtom IR Phase 2 方向）

### 5. ~~父子结构折叠（code-atom-ir §3.3）~~ ✅ 已完成（本 commit）
- 已修：`MergePhysical` 对"可折叠容器（FUNCTION/STRUCT）包含子原子"的场景不再吞并子级，而是保留两者并将容器 `MaxLevel` 降为 L1 骨架；`CropBudget` 从 MaxLevel 起试装。

### 6. ~~symbol 原子 L0 载荷~~ ✅ 已完成（本 commit）
- 已修：symbol 原子 Top-5 经 `GetFullDefinition` 抓 definition 作 L0 载荷（延迟护栏）。实测 `intent=definition` 查 device_probe：5 个 L0 完整实现体 + 1 个 L1，7.7KB/8KB 预算。

### 7. ~~rg 命中 ±2 行上下文扩展~~ ✅ 已完成（本 commit）
- 已修：snippet 原子按匹配行 ±2 行扩展（`snippetExpander`，带每文件行偏移缓存），相邻命中窗口经物理吞并自动合并；Signature 保持单行用于 L1 降级。实测 u-boot TODO：相邻 TODO 窗口合并后原子数 1683→1459，Kconfig 命中带完整 help 上下文。

### 8. ~~交错块 LCA 合并~~ ✅ 已完成（务实方案）
- 已修：同一棵树的 AST 节点只会相离或包含，部分交错只发生在 rg 窗口跨进 AST 节点时——`MergePhysical` 改为交错时按 Priority 取舍（高优先级胜），完整 LCA 重解析合并收益过低，不再立项。

### 9. USR 全局符号身份
- **内容**：`SemanticID` 从 `name@path` 升级为 clangd USR，实现跨文件真去重。
- **难点**：LSP 协议不暴露 USR；只能读 clangd 索引分片（`.cache/clangd/index`，格式非稳定 ABI）或换 C++ 侧通道，成本高。

### 10. ~~B10 缓存按文件失效~~ ✅ 已完成（本 commit）
- 已修：缓存条目携带文件依赖集（`SetWithFiles`，unified 结果从原子收集，含被裁剪丢弃的贡献文件）；watcher `OnFileChange` 传文件 URI，`Router.InvalidateFile` 只失效依赖该文件的条目；无依赖信息的条目保守失效。

### 11. ~~QueryDirectory 全仓 AST 解析性能~~ ✅ 已完成（本 commit）
- 已修：worker pool 并发解析（每 worker 独立 parser + 查询只编译一次）+ 无效 CSP 模式预编译失败快速返回。实测 u-boot 全仓 13k 文件 struct_specifier 扫描 2.8s（19 万+ 命中）；无效模式 0.12s 报错（原行为：全仓慢扫一遍后静默返回空）。auto 融合路径对"非 CSP 查询"保持静默跳过（不污染统一结果）。

### 12. ~~callers 压测对象修正~~ ✅ 已完成
- 已修：换 `srsran::byte_buffer::append`（1415 处引用）重测，depth=3 → 1329 调用者 / 16.6KB（触发截断）。数据已回填 benchmark §6。

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
- **include 映射降级**：compile_commands.json → IncludeMap（含全局目录无区分度过滤）；symbol 降级 rg 按 include 邻域限定（≤400 文件）；ast 层锚定 filePath 时邻域扩展（≤20 文件）——本 commit
