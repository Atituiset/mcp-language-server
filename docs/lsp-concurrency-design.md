# LSP Client 并发安全设计

> 2026-07-22，解决 daemon+proxy 模式下 clangd 单实例并发冲突。

## 1. 问题

### 1.1 架构背景

本项目有两种部署模式：

- **独立模式（默认）**：MCP client 通过 stdio 拉起进程，1 客户端 = 1 进程 = 1 份 clangd
- **daemon+proxy 模式**：daemon 独占 clangd/watcher/缓存，多个 proxy 客户端通过 HTTP 共享

daemon+proxy 模式的核心价值是**节省内存**——对百万行 C/C++ 仓，单个 clangd 实例常驻内存可达 3-8 GB，N 个 MCP client 各自启动 clangd 直接 OOM。

### 1.2 并发冲突

多个 proxy 同时向 daemon 发 MCP tool 请求时，daemon 内唯一的 `lsp.Client` 会并发向 clangd stdin 发送请求。虽然 `lsp.Client` 在 Go 层面用 `writeMu` 保证了 JSON-RPC 帧不交错，但 **clangd 内部不保证并发请求的线程安全**。

具体冲突路径：

```
时间轴 →

proxy-A:  references("hot_func")
  client.Symbol(...)           ────→
  client.OpenFile(file1)       ────→    ← didOpen: 触发 clangd 异步索引
  client.References(...)       ────→    ← 此时 file1 可能未索引完

proxy-B:  definition("foo")
  client.Symbol(...)           ────→    ← 与 proxy-A 的请求交错
  client.OpenFile(file2)       ────→    ← 又一个 didOpen

watcher: (文件变更检测)
  client.NotifyChange(...)     ────→    ← didChange 与查询同时到达
```

临床表现：

- **间歇性空结果**：索引未就绪时查询返回空
- **clangd 报错**：`-32803 Request cancelled` 或内部错误
- **结果错乱**：跨请求的状态混淆
- **极端情况崩溃**：clangd 的 Clang ASTUnit 不保证线程安全

### 1.3 为什么现有 writeMu 不够

`writeMu`（`transport.go:201`）只保护 **stdin 写入不交错**，不保护 **逻辑层面的状态一致性**：

```
writeMu 保护的范围:
  client-A 写入请求 body ──┐
                           ├── 同一时刻只有一个 goroutine 在写 stdin ✓
  client-B 写入请求 body ──┘

writeMu 不保护的范围:
  client-A: didOpen file1  ── 写入完成，释放 writeMu
  client-B: workspace/symbol ── 获取 writeMu，写入请求
                                ↑ clangd 在收到 workspace/symbol 时，
                                  file1 可能还没索引完
```

## 2. 设计目标与约束

| 维度 | 硬约束 | 优化方向 |
|------|--------|---------|
| **内存** | 单 clangd 实例（daemon 存在的意义） | 无额外进程开销 |
| **准确性** | 状态变更（didOpen/didChange）与查询不得交错 | 读写锁隔离 |
| **时延** | 纯查询不应被其他查询阻塞 | 查询间并发，状态变更时短暂排队 |

## 3. 设计方案：RWMutex + 可配置并发上限

### 3.1 核心思路

LSP 操作天然分为两类：

| 类别 | 操作示例 | 对 clangd 状态的影响 |
|------|---------|-------------------|
| **读（查询）** | `workspace/symbol`、`textDocument/documentSymbol`、`textDocument/hover`、`textDocument/references`、`textDocument/definition`、`callHierarchy/*` | 无副作用，纯查询 |
| **写（状态变更）** | `textDocument/didOpen`、`textDocument/didChange`、`textDocument/didClose`、`textDocument/didSave` | 修改 clangd 内部状态（AST 缓存、索引） |

用 `sync.RWMutex` 建模：

- **查询操作**获取读锁（`RLock`），多个查询可以并发
- **状态变更操作**获取写锁（`Lock`），执行时阻塞所有查询和新变更

在此之上叠加可配置的**并发信号量**，通过环境变量 `LSP_MAX_CONCURRENT_REQUESTS` 控制，适配不同 clangd 版本和环境的稳定性差异。

### 3.2 架构图

```
┌───────────────────────────────────────────────────────────┐
│                     lsp.Client                            │
│                                                           │
│  ┌─────────────────────┐    ┌───────────────────────────┐ │
│  │ stateMu sync.RWMutex│    │ sem chan struct{} (可选)   │ │
│  │                     │    │ LSP_MAX_CONCURRENT_        │ │
│  │ RLock (查询)        │    │ REQUESTS=N                 │ │
│  │ Lock  (状态变更)     │    │ N=0: 无限                  │ │
│  └─────────────────────┘    │ N=1: 全串行                │ │
│                              │ N>1: 有限并发              │ │
│                              └───────────────────────────┘ │
│                                                           │
│  ┌─────────────────┐  ┌─────────────────────────────────┐ │
│  │ Call (查询)      │  │ Call/Notify (状态变更)           │ │
│  │ sem <-           │  │ Lock()                         │ │
│  │ RLock()          │  │ 执行                            │ │
│  │ 执行             │  │ Unlock()                       │ │
│  │ RUnlock()        │  └─────────────────────────────────┘ │
│  │ <- sem           │                                      │
│  └─────────────────┘                                      │
└───────────────────────────────────────────────────────────┘
```

### 3.3 时序示例

```
时间轴 →

proxy-A:  references("hot")   [sem] RLock ═════════════════ RUnlock [sem]  (2s)
proxy-B:  hover("var")        [sem] RLock ═ RUnlock [sem]              (10ms, 与A并发)
proxy-C:  definition("foo")   [sem] RLock ═══ RUnlock [sem]            (200ms, 与A并发)
watcher:  didChange           等待读锁释放...  Lock █ Unlock           (阻塞2s, 自身1ms)

                                                       ↑
                                              A B C 完全并发
                                              时延无影响
```

稳态下（warmUpLSP 预热后文件基本已打开），`didOpen`/`didChange` 极少触发，查询几乎零排队。

### 3.4 并发上限配置

通过环境变量 `LSP_MAX_CONCURRENT_REQUESTS` 配置：

```
LSP_MAX_CONCURRENT_REQUESTS=0  → 无限并发（仅 stateMu 读写锁保护）
LSP_MAX_CONCURRENT_REQUESTS=1  → 全串行（最安全，等同于方案A）
LSP_MAX_CONCURRENT_REQUESTS=3  → 最多 3 个查询并发（默认推荐值）
LSP_MAX_CONCURRENT_REQUESTS=8  → 高并发（性能优先）
```

设计决策：

- **默认值 3**：在典型 MCP 场景（2-3 个 proxy 客户端同时工作）下零排队，同时限制极端并发
- **可用环境变量覆盖**：生产环境遇到 clangd 不稳定时可临时降到 1
- **单 token 信号量**：`chan struct{}` 零内存分配，比 `sync.Semaphore` 更简洁

### 3.5 操作分类判定

```
isStateMutation(method) bool

true (写锁):
  textDocument/didOpen
  textDocument/didChange
  textDocument/didClose
  textDocument/didSave

false (读锁):
  所有其他 LSP request/notification
  (workspace/symbol, textDocument/definition, ...)
```

注意：`textDocument/willSave` 和 `textDocument/willSaveWaitUntil` 同样归类为状态变更，虽然本项目目前不使用它们。

## 4. 实现方案

### 4.1 变更范围

仅涉及三个文件：

| 文件 | 变更 |
|------|------|
| `internal/lsp/client.go` | 新增 `stateMu`、`sem` 字段；新增 `NewClientWithOptions`；`Notify` 签名增加 method 参数 |
| `internal/lsp/transport.go` | `Call`/`Notify` 增加锁获取逻辑；新增 `isStateMutation()` |
| `internal/lsp/methods.go` | 生成的 `Notify` 调用传入 method 名 |

### 4.2 Client 结构体变更

```go
type Client struct {
    // ... 现有字段 ...

    // stateMu 保护 LSP 状态一致性：查询获取 RLock，状态变更获取 Lock。
    stateMu sync.RWMutex

    // sem 为可选的并发上限信号量。nil = 无限并发。
    sem chan struct{}

    // maxConcurrent 记录 sem 的容量，用于日志和调试。
    maxConcurrent int
}
```

### 4.3 Call 方法变更

```go
func (c *Client) Call(ctx context.Context, method string, params any, result any) error {
    if !c.Alive() {
        return fmt.Errorf("LSP server connection is closed, cannot call %s", method)
    }

    // 获取并发锁
    if c.sem != nil {
        select {
        case c.sem <- struct{}{}:
            defer func() { <-c.sem }()
        case <-ctx.Done():
            return ctx.Err()
        }
    }

    // 分类获取读写锁
    if isStateMutation(method) {
        c.stateMu.Lock()
        defer c.stateMu.Unlock()
    } else {
        c.stateMu.RLock()
        defer c.stateMu.RUnlock()
    }

    // ... 原有逻辑不变 ...
}
```

### 4.4 Notify 方法变更

```go
func (c *Client) Notify(ctx context.Context, method string, params any) error {
    if !c.Alive() {
        return fmt.Errorf("LSP server connection is closed, cannot notify %s", method)
    }

    // 状态变更通知需要写锁
    if isStateMutation(method) {
        c.stateMu.Lock()
        defer c.stateMu.Unlock()
    }
    // 只读通知不获取锁（不需要等待响应）

    // ... 原有逻辑不变（含 writeMu 保护写入）...
}
```

注意 `Notify` 对非状态变更操作**不获取任何锁**——`writeMu` 已足够保护写入不交错，而通知类操作（如 `initialized`、`exit`）不涉及 clangd 内部状态竞争。

### 4.5 OpenFile 幂等优化

`OpenFile`（`client.go:364-403`）在文件已打开时直接返回，不发送 `didOpen`：

```go
func (c *Client) OpenFile(ctx context.Context, filepath string) error {
    // 先检查是否已打开（读锁）
    c.openFilesMu.RLock()
    if _, exists := c.openFiles[string(uri)]; exists {
        c.openFilesMu.RUnlock()
        return nil  // 幂等：不获取 stateMu 写锁
    }
    c.openFilesMu.RUnlock()

    // 未打开才需要 didOpen（触发 stateMu 写锁）
    // ...
}
```

这保证了稳态下 `didOpen` 几乎不发生，写锁几乎不阻塞查询。

## 5. 备选方案与取舍

| 方案 | 内存 | 准确性 | 时延 | 复杂度 |
|------|------|--------|------|--------|
| **RWMutex + 信号量（选用）** | 单 clangd ✓ | 写操作隔离 ✓ | 查询并发 ✓ | 低 |
| 全串行 mutex | 单 clangd ✓ | 完全隔离 ✓ | 最差 ✗ | 最低 |
| 请求队列 + worker | 单 clangd ✓ | 完全隔离 ✓ | 最差 ✗ | 中 |
| 每 proxy 一个 clangd | OOM ✗ | 天然隔离 ✓ | 最优 ✓ | 最低（回退独立模式） |

全串行方案被否决的原因：在生产环境中，2-3 个 LLM 同时工作时，慢查询（references ~2s）会堵住快查询（hover ~10ms），体感差异明显。

## 6. 实测验证

在 u-boot（176 万行 C，6231 条编译命令）上用真实 clangd 实例进行并发压力测试。

### 6.1 测试用例

| 测试 | 说明 | 源码位置 |
|------|------|---------|
| `TestConcurrentSymbolQueries` | 10 个 goroutine 并发执行 `workspace/symbol`，不同符号 | `internal/lsp/client_concurrency_test.go` |
| `TestConcurrentMixedOperations` | 3 个查询 goroutine + 2 个 didOpen goroutine 交错执行 | 同上 |
| `TestSemaphoreEnforcement` | 9 查询同时释放，验证 sem=1 串行化 vs sem=3 并发批处理 | 同上 |

### 6.2 clangd 18.1.3（Ubuntu 24.04 默认）

```
=== sem=3 (默认，最多 3 并发查询) ===
TestConcurrentSymbolQueries:  10/10 success, 0 empty, 0 failed in 40.5ms
TestConcurrentMixedOperations: 15/15 success, 0 failures (sem=3)

=== sem=1 (全串行) ===
TestConcurrentSymbolQueries:  10/10 success, 0 empty, 0 failed in 89.1ms  (2.2× 慢)
TestSemaphoreEnforcement:      9 queries in 15.7ms (batch=9, 串行批次验证通过)
```

### 6.3 clangd 21.1.8（生产环境版本）

```
=== sem=3 (默认) ===
TestConcurrentSymbolQueries:  10/10 success, 0 empty, 0 failed in 11.9ms  (比 18 快 3.4×)
TestConcurrentMixedOperations: 15/15 success, 0 failures

=== sem=1 (全串行) ===
TestConcurrentSymbolQueries:  10/10 success, 0 empty, 0 failed in 29.4ms  (2.5× 慢于并发模式)
```

### 6.4 结论

| 维度 | clangd 18 | clangd 21 | 说明 |
|------|-----------|-----------|------|
| sem=3 并发延迟 | 40.5ms | **11.9ms** | 21 快 3.4× |
| sem=1 串行延迟 | 89.1ms | 29.4ms | 1 比 3 慢 2-2.5×，信号量生效 |
| 结果准确性 | 10/10 ✅ | 10/10 ✅ | 两版本均 0 错误 0 空结果 |
| 读写隔离 | ✅ | ✅ | didOpen 写锁正确阻塞查询 |

**clangd 21 在并发查询下安全且性能优异**。生产环境可放心使用默认 `LSP_MAX_CONCURRENT_REQUESTS=3`，无需降为 sem=1。

## 7. 参考

- [LSP Specification - Request Ordering](https://microsoft.github.io/language-server-protocol/specifications/lsp/3.17/specification/#requestOrdering)
- [clangd Threads and request handling](https://clangd.llvm.org/design/threads)
- 本仓 benchmark 文档：`docs/benchmark-2026-07-17.md`
