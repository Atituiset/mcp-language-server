# Daemon + Proxy：多进程客户端共享单个 LSP 实例（设计稿，待审核）

> 状态：**已实施**（2026-07-20 设计稿经审核后落地，daemon.go / proxy.go / main.go 子命令分发）
> 关联问题：生产环境多进程 MCP client → N 份 LSP 子进程 → OOM

---

## 1. 问题陈述

当前部署形态（v0.3.0）：MCP client 通过 stdio 拉起 `mcp-language-server`，形成固定三元组：

```
1 个 MCP client 进程 ── 1 个 mcp-language-server 进程 ── 1 个 LSP server 进程
```

在单客户端场景（Claude Desktop / 单 agent）这没有问题。但生产环境**多进程 MCP client**（多个 agent worker 各自拉起自己的 MCP server）时，三元组被整体复制 N 份：

- N 个 mcp-language-server（各自 ~百 MB，可接受）
- **N 个 LSP server**——clangd 在百万行仓上常驻 **GB 级**内存（索引 + AST 缓存），这是 OOM 的直接来源

LSP 协议本身无法解决：clangd/gopls 都是单客户端 stdio 协议，不存在"多个客户端共享一个 clangd"的用法。因此**共享必须做在 MCP 层**：让 LSP 前面的 mcp-language-server 只有一个。

## 2. 方案总览

拆成两个角色，新增两个子命令：

```
客户端进程A ──stdio──> proxy A ──┐
客户端进程B ──stdio──> proxy B ──┤ HTTP (streamable, 127.0.0.1 回环)
客户端进程C ──stdio──> proxy C ──┘            │
                                             ▼
                          daemon（每 workspace 全局唯一）
                          独占：LSP server / watcher / 搜索缓存
                                             │ stdio
                                             ▼
                                      单个 LSP server
```

- **daemon**：长驻进程。复用现有全部初始化路径（LSP 启动 → warmup → 工具注册），
  只是把传输从 stdio 换成 streamable HTTP（mcp-go v0.54.1 原生支持，go.mod 已依赖）。
  所有客户端会话共享同一个 Router/缓存/LSP——**缓存跨客户端复用是额外收益**
  （A 查过的 search，B 直接命中）。
- **proxy**：每客户端一个的轻量桥。对 MCP client 表现为普通 stdio MCP server
  （工具面经 daemon 的 `tools/list` 镜像，完全一致，含 `Meta.ui` 资源指向），
  收到调用后通过 streamable HTTP 转发给 daemon。内存占用可忽略，随时可以死。
- **无子命令的现有用法完全不变**（`mcp-language-server --workspace X --lsp clangd`
  仍是独立 stdio 模式），单客户端用户零感知。

## 3. 命令设计

```bash
# daemon：每 workspace 一个，长驻
mcp-language-server daemon --workspace <repo> --lsp clangd \
    [--addr 127.0.0.1:0] [--idle-timeout 30m] [-- <lsp-args>]

# proxy：给 MCP client 当 stdio server 用；daemon 不存在时自动拉起
mcp-language-server proxy --workspace <repo> --lsp clangd [-- <lsp-args>]

# 现状模式（默认，不变）
mcp-language-server --workspace <repo> --lsp clangd [-- <lsp-args>]
```

MCP client 配置只需把原来的 `mcp-language-server` 命令换成 `mcp-language-server proxy`，
参数不变。

## 4. 关键设计点

### 4.1 daemon

| 设计点 | 决策 | 理由 |
|--------|------|------|
| 传输 | streamable HTTP，`net.Listen("tcp", "127.0.0.1:0")` 拿临时端口，自建 `http.Server` 挂 `StreamableHTTPServer`（mcp-go 已实现 `http.Handler`） | 多会话是 mcp-go 内建能力；`:0` 免端口冲突 |
| 绑定地址 | **仅回环**；解析 `--addr` 的 host，非 loopback 直接拒绝启动 | 无鉴权，本机即信任边界；防误暴露 |
| 会话发现 | 会话文件 `~/.cache/mcp-language-server/daemon-<sha1(workspace)[:12]>.json`（0600）：`{pid, addr, workspace, lsp, args, startedAt}`；启动写、退出删 | proxy 凭 workspace 即可找到 daemon，无需配置端口 |
| 防重复 | ① proxy 侧 flock 串行化"检查+拉起"（见 4.2）；② daemon 启动时发现已有存活会话文件则退出并报地址 | 双保险，竞态窗口内也不会起双份 |
| 生命周期 | **禁用父进程自杀监控**（daemon 就该比客户端活得久）；SIGTERM/SIGINT 走现有优雅关闭（关文件 → shutdown → exit → 2s 强杀） | — |
| 空闲回收 | 会话钩子（`Hooks.AddOnRegisterSession/AddOnUnregisterSession`）维护活跃计数；计数为 0 持续 `--idle-timeout`（默认 30m）→ 优雅退出，连带释放 clangd | 防止 daemon+clangd 泄漏成新的内存问题 |
| 崩溃残留 | 会话文件可能 stale（kill -9）→ proxy 侧 pid 存活 + TCP 拨号双重校验，stale 即重拉 | — |

### 4.2 proxy

1. **ensureDaemon**（带锁拉起）：
   - 读会话文件 → pid 存活 && TCP 可拨通 → 直接用；
   - 否则对 `<workspace 对应 lockfile>` 加 `syscall.Flock`，锁内复查（防 thundering herd），
     `os.StartProcess`（`Setsid` 脱离进程组）拉起 daemon，轮询会话文件出现（≤60s，
     覆盖 clangd 冷启动索引时间）。
2. **桥接**：
   - `client.NewStreamableHttpClient(daemonAddr)` → Initialize；
   - `ListTools` → 在本地 stdio MCPServer 上原样 `AddTool`，handler 统一 passthrough
     （`CallTool(name, 原参数)`）；`ListResources` 同理 passthrough `ReadResource`；
   - daemon→client 的通知（`OnNotification`）经 `SendNotificationToAllClients` 转发到 stdio 侧。
3. **生命周期**：stdin EOF（ServeStdio 返回）即退出——proxy 死了不影响 daemon 和其他客户端。

### 4.3 并发安全

daemon 内所有共享状态已是并发安全的（v0.3.0 的修复）：LSP stdin 有 `writeMu`、
请求按 ID 分发、缓存有读写锁 + 反向索引、router 无共享可变状态（管道全在请求局部变量上）。
mcp-go 的 MCPServer 本身按会话分发并发请求。无需新增同步原语。

### 4.4 安全边界

- HTTP 端点**无鉴权**，因此只允许回环绑定（4.1）。
- 编辑工具仍默认关闭（`MCP_LS_ENABLE_EDITS`），daemon 默认面是纯只读——
  即使本机其他用户/进程误连也无法改代码（127.0.0.1 上不同用户可达，
  若需进一步隔离后续可加 unix socket，本期不做）。

## 5. 实施清单

| # | 改动 | 文件 | 量级 |
|---|------|------|------|
| 1 | `parseConfig` 抽出 `config.validate()` + FlagSet 化；`start()` 拆出 `setup()`（LSP/router/工具注册，daemon 复用）；main 加子命令分发 | main.go | ~80 行改动 |
| 2 | daemon：HTTP 传输、会话文件、idle reaper、禁父进程监控 | daemon.go（新） | ~220 行 |
| 3 | proxy：ensureDaemon（flock + spawn）+ 镜像桥接 | proxy.go（新） | ~300 行 |
| 4 | 单测：会话文件读写/stale 判定/锁内复查 | daemon_test.go（新） | ~150 行 |
| 5 | 真实 e2e 冒烟脚本（见 §6） | /tmp（不入仓） | — |
| 6 | 文档：README 多进程用法、CLAUDE.md/架构文档"部署模式"节、design-deep-dive §8 更新 | 各文档 | — |

**明确不做**：鉴权/TLS、unix socket、LSP 层/router/atom 管道任何逻辑改动、
daemon 热升级、单 daemon 多 workspace。

## 6. 验证计划

单测 + 回归之外，用本仓（2.3 万行 Go）+ gopls 做真实 e2e：

1. `daemon --idle-timeout 20s` 启动，等会话文件出现；
2. 并发起 **2 个 proxy**，各自 initialize → tools/list（=7）→ search，断言两路结果一致；
3. **`ps` 断言全局只有 1 个 gopls 进程**（核心验收：多客户端共享单 LSP）；
4. 杀光 proxy，等 20s+，断言 daemon 与 gopls 均已退出（idle 回收）；
5. 再发起 proxy → 断言 daemon 被自动拉起（ensureDaemon）；
6. u-boot + clangd 复测搜索/调用链（确认 daemon 形态下大仓行为不变）。

## 7. 风险与缓解

| 风险 | 缓解 |
|------|------|
| daemon 崩溃 → 全部客户端失去符号层 | proxy 调用失败即重走 ensureDaemon（flock 内重启）；且符号层降级 WARNING 机制本就存在 |
| 多客户端并发重查询打到同一 LSP | 并发安全已保证；搜索预算闸（8KB/12KB/16KB）限制单请求产出；共享缓存反而削峰 |
| 会话文件 stale | pid + 拨号双校验 |
| daemon 自身 OOM（单点承载全部客户端） | 与现状单进程一致：LSP 是绝对大头，daemon 本身增量可忽略；且 N→1 后总内存只会下降 |
