# 基线压测报告：大型 C/C++ 测试仓（2026-07-17）

> 对应《docs/handoff-tasks.md》任务 C。压测代码版本 `b4d886f`（含 B1–B7 工具面收敛 + ripgrep 解析修复），对照基线 `cc756da`。
> 测试仓位置：`~/Projects/testbeds/`（不入本仓）。

## 1. 环境

| 项 | 值 |
|----|----|
| CPU | 18 核 |
| clangd | Ubuntu 18.1.3 |
| gcc | 13.3.0（host，u-boot sandbox 用） |
| ripgrep | 14.1.0 |
| mcp-language-server | `b4d886f`（对照组 `cc756da`） |

## 2. 测试仓接入

| 仓 | 版本 | 实测 LOC | compile_commands.json | 生成方式 |
|----|------|---------|----------------------|---------|
| u-boot | `ece349ad`（v2026.07，浅克隆） | **176 万**（.c 119 万 + .h 53 万 + .S 3.4 万，13405 文件） | 1246 条 | `make sandbox_defconfig` + `make -j18` + `python3 scripts/gen_compile_commands.py` |
| srsRAN_Project | `d2f4b70`（tag `release_25_10`，浅克隆） | **100 万**（.cpp 92 万，4680 文件） | 1116 条 | `cmake -GNinja -DCMAKE_EXPORT_COMPILE_COMMANDS=ON` |

接入踩坑（均已解决，细节备查）：

- **srsRAN GitHub 默认分支已清空**：项目 2025-12 迁移至 OCUDU（gitlab.com/ocudu/ocudu），`master` 只剩归档 README。必须按 tag 克隆（`--branch release_25_10`）。
- **无 root 依赖安装**：bison/flex/m4/swig（u-boot 构建）与 libmbedtls/libfftw3/libsctp/libyaml-cpp/libgtest（srsRAN cmake）均通过 `apt-get download` + `dpkg -x` 解压在 `~/Projects/testbeds/.toolchain/root/`，用 `PATH`/`BISON_PKGDATADIR`/`M4`/`SWIG_LIB`/`CMAKE_PREFIX_PATH`/`PKG_CONFIG_PATH` 环境变量指过去。
- **u-boot 构建两处裁剪**：`CONFIG_TOOLS_MKEFICAPSULE=n`（缺 gnutls 头）、`CONFIG_EFI_CAPSULE_AUTHENTICATE=n`（缺 efitools 的 cert-to-efi-sig-list）。最终 binman 打包阶段报错不影响 .cmd 文件生成，compile_commands 完整。
- **clangd 后台索引需 didOpen 触发**（重要）：clangd 18 在 initialize/initialized 之后**不会**自动开始后台索引，直到第一个 `textDocument/didOpen`。mcp-language-server 的符号层工具在此之前会一直降级 ripgrep。压测中通过先调一次 `diagnostics`（会 OpenFile）预热解决。详见 §5 建议。

## 3. 压测结果（修复前 `cc756da` vs 修复后 `b4d886f`，u-boot 仓）

| 指标 | 修复前 | 修复后 | 结论 |
|------|--------|--------|------|
| tools/list 数量 | 17 | **9**（`MCP_LS_DEBUG_TOOLS=1` 时 17） | B4 生效 |
| `search`（auto，query=TODO）返回体积 | **22,404 B，无截断**（三层 5572 行拼接） | **3,956 B，带截断标注**（每层 ≤50 行/4KB） | B2 生效，体积降 82% |
| 缓存串扰（同 query 不同 intent） | **复现**：intent=todo → text(328)，intent=function → **也返回 text(328)**（命中前者缓存） | intent=todo → text(327)，intent=function → **ast(1)** | B1 修复验证 |
| 符号层降级标注 | 降级 ripgrep 仍标 `symbol`（误导） | 标 `symbol-fallback-text` + WARNING 头 | B3 生效 |
| 文本层结果内容 | 全部为 `0: ` 空行（解析 bug） | 真实 `文件/行号/内容` | 见 §4 |
| `callers` device_probe（u-boot 最热入口） | —（列号 bug 未测） | depth=2：**146 个调用者，14.1 KB**；depth=3：**355 个，34.3 KB** | B5 clamp=3 下体积有界 |
| `callers` handle_rrc_setup_request（srsRAN） | — | depth=2/3：**1 个调用者，189 B**（RRC 消息入口由统一 dispatch 调用，符合预期） | — |

srsRAN 仓 `search`（auto，query=TODO）：2,264 B 带截断（symbol 层已就位，1 结果）。

## 4. 压测发现并修复的问题

**ripgrep JSON 解析 bug（`b4d886f` 已修）**：`internal/tools/ripgrep.go` 的 `parseRipgrepOutput` 按顶层字段解析 `--json` 输出，但 ripgrep 的 schema 把 `path`/`lines`/`line_number` 全部嵌在 `data` 包裹层下。结果是**文本层（L1）搜索结果全部退化为 `0: ` 空行**——3229 个"结果"没有一个字节的有效内容。该 bug 在 176 万行真实仓上才暴露（单元测试此前零覆盖，已补 `internal/tools/ripgrep_test.go`）。

## 5. clangd 冷启动索引耗时（清空 `.cache/clangd` 后实测）

| 仓 | 翻译单元 | 索引分片 | 全量索引完成耗时 | 说明 |
|----|---------|---------|----------------|------|
| u-boot | 1246 | 2109 | **31.0 s** | C 代码，预热 didOpen 后立即开始 |
| srsRAN | 1116 | 3625 | **235.3 s** | 现代 C++，模板重，约 4 分钟 |

另测得：被 didOpen 的文件自身符号 **0.2 s 内**即可经 `workspace/symbol` 命中（动态索引，无需等全量）。即"打开即查本文件，一分钟内查全仓（C）/四分钟（C++）"。

## 6. 观察与后续建议

1. **server 侧 warmup（建议 P1）**：启动后对任一源文件主动发一次 didOpen（或文档化要求客户端先调 `diagnostics`/`hover`），否则 clangd 永远不开始后台索引，符号层长期停在 `symbol-fallback-text`。
2. **callers 输出无截断**：depth=3 在 device_probe 上达 34 KB。当前仅有 depth clamp=3，未做字节级截断（search 有 12KB 总量闸，call_hierarchy 没有）。热点函数上仍偏大，建议后续按 `docs/code-atom-ir.md` 的降级思想加结果数/字节上限。
3. **srsRAN callers 仅 1 命中**：`handle_rrc_setup_request` 由统一消息 dispatch 调用，调用链浅属真实结构；做调用链压测时应选更热的工具函数（如 `srslog` 或 byte_buffer 系列）。
4. u-boot 的 `sandbox_defconfig` 仅覆盖核心代码路径；验证单板宏分支需换真实 defconfig（如 `stm32mp15_defconfig`，需 ARM 交叉工具链，本机未装）。
