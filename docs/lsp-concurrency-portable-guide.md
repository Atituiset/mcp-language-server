# LSP Client 并发安全：可移植实现指南

> 提取自 mcp-language-server 的 clangd 并发安全方案，适用于任何共享单 LSP 实例的多客户端架构。

## 一、问题本质

```
多个客户端 ──→ 同一个 LSP 进程（clangd/gopls/rust-analyzer/...）
                    ↑
              并发请求在此冲突
```

LSP 协议（JSON-RPC 2.0）支持并发请求，但多数 LSP 服务器实现**不保证内部线程安全**。特别是：
- 状态变更通知（didOpen/didChange）修改索引/AST 缓存
- 查询请求（workspace/symbol、references）读取索引/AST 缓存
- 两者交错 → 竞态 → 空结果 / 错误 / 崩溃

---

## 二、核心设计：三层保护

```
┌──────────────────────────────────────────────────────┐
│              LSP Client (Go/其他语言)                  │
│                                                      │
│  Layer 1: writeMu (Mutex)    已有：保护帧不交错       │
│  Layer 2: stateMu (RWMutex)  新增：读写语义隔离       │
│  Layer 3: sem   (Semaphore)  新增：并发硬上限          │
└──────────────────────────────────────────────────────┘
```

### Layer 1 — 写入串行化（已有，必须保留）

```go
// 已有实现：多个 goroutine 可能同时写 stdin
func writeMessage(msg *Message) error {
    writeMu.Lock()
    defer writeMu.Unlock()
    // header + body 两次 write 必须原子
    fmt.Fprintf(stdin, "Content-Length: %d\r\n\r\n", len(body))
    stdin.Write(body)
}
```

### Layer 2 — 读写锁隔离（核心新增）

**原则**：把 LSP 方法分为两类

| 分类 | 方法 | 锁类型 | 并发行为 |
|------|------|--------|---------|
| **查询（读）** | workspace/symbol, textDocument/definition, references, hover, documentSymbol, callHierarchy/*, ... | RLock | 多个可并发 |
| **状态变更（写）** | textDocument/didOpen, didChange, didClose, didSave | Lock | 独占，阻塞所有查询 |

```go
// 判定函数（Copy this exactly）
func isStateMutation(method string) bool {
    switch method {
    case "textDocument/didOpen",
         "textDocument/didChange",
         "textDocument/didClose",
         "textDocument/didSave",
         "textDocument/willSave",
         "textDocument/willSaveWaitUntil":
        return true
    }
    return false
}
```

```go
// Call 方法中获取锁
func (c *Client) Call(ctx context.Context, method string, params, result any) error {
    if isStateMutation(method) {
        c.stateMu.Lock()         // 写锁：等所有查询完成
        defer c.stateMu.Unlock()
    } else {
        c.stateMu.RLock()        // 读锁：多个查询可并发
        defer c.stateMu.RUnlock()
    }
    // ... 原有逻辑：分配 ID、写请求、等响应 ...
}

// Notify 方法中获取锁（仅状态变更需要）
func (c *Client) Notify(ctx context.Context, method string, params any) error {
    if isStateMutation(method) {
        c.stateMu.Lock()
        defer c.stateMu.Unlock()
    }
    // 非状态变更的 Notify（如 initialized、exit）不获取 stateMu
    // writeMu 已足够保护写入
    // ... 原有逻辑 ...
}
```

### Layer 3 — 并发上限信号量（可选但有价值）

即使查询间可以并发，也要限制上限，防止极端场景：

```go
// 在 Client 结构体中
type Client struct {
    // ...
    stateMu sync.RWMutex
    sem     chan struct{}  // nil = 无限制
}

// 在 NewClient 中读取环境变量初始化
func NewClient(...) *Client {
    maxConc := 3  // 默认值
    if v := os.Getenv("LSP_MAX_CONCURRENT_REQUESTS"); v != "" {
        maxConc, _ = strconv.Atoi(v)
    }
    var sem chan struct{}
    if maxConc > 0 {
        sem = make(chan struct{}, maxConc)
    }
    return &Client{sem: sem, ...}
}

// 在 Call 开头获取信号量
func (c *Client) Call(...) error {
    if c.sem != nil {
        select {
        case c.sem <- struct{}{}:        // 获取令牌
            defer func() { <-c.sem }()   // 释放令牌
        case <-ctx.Done():
            return ctx.Err()
        }
    }
    // ... 然后是 stateMu 锁 + 原有逻辑
}
```

---

## 三、整个 Call 方法的完整顺序

```go
func (c *Client) Call(ctx context.Context, method string, params any, result any) error {
    // 0. 存活检查
    if !c.Alive() { return ErrClosed }

    // 1. 并发上限（可选）
    if c.sem != nil {
        c.sem <- struct{}{}
        defer func() { <-c.sem }()
    }

    // 2. 读写锁
    if isStateMutation(method) {
        c.stateMu.Lock()
        defer c.stateMu.Unlock()
    } else {
        c.stateMu.RLock()
        defer c.stateMu.RUnlock()
    }

    // 3. 分配请求 ID
    id := c.nextID.Add(1)

    // 4. 注册响应 channel
    ch := make(chan *Response, 1)
    c.handlers[id] = ch
    defer delete(c.handlers, id)

    // 5. 写入请求（writeMu 保护）
    c.writeMessage(buildRequest(id, method, params))

    // 6. 等待响应或超时
    select {
    case resp := <-ch:
        // 处理响应
    case <-ctx.Done():
        return ctx.Err()
    }
}
```

---

## 四、不同语言的移植要点

### Go

```go
// 结构体字段
stateMu sync.RWMutex
sem     chan struct{}

// 锁获取
if isStateMutation(method) {
    c.stateMu.Lock()
    defer c.stateMu.Unlock()
} else {
    c.stateMu.RLock()
    defer c.stateMu.RUnlock()
}
```

### Rust

```rust
use std::sync::{RwLock, Arc};
use tokio::sync::Semaphore;

struct LspClient {
    state_lock: RwLock<()>,     // 使用 () 作为守卫
    sem: Option<Arc<Semaphore>>,
}

fn call(&self, method: &str) {
    let _permit = if let Some(s) = &self.sem {
        Some(s.acquire().await)
    } else { None };

    if is_state_mutation(method) {
        let _guard = self.state_lock.write().unwrap();
        // ... do work ...
    } else {
        let _guard = self.state_lock.read().unwrap();
        // ... do work ...
    }
}
```

### Python (asyncio)

```python
import asyncio

class LspClient:
    def __init__(self, max_concurrent=3):
        self._state_lock = asyncio.Lock()        # 写锁
        self._reader_count = 0                    # 读计数器
        self._reader_cond = asyncio.Condition()   # 读锁条件变量
        self._sem = asyncio.Semaphore(max_concurrent)

    async def _acquire_read(self):
        async with self._reader_cond:
            await self._reader_cond.wait_for(
                lambda: not self._state_lock.locked()
            )
            self._reader_count += 1

    async def _release_read(self):
        async with self._reader_cond:
            self._reader_count -= 1
            if self._reader_count == 0:
                self._reader_cond.notify_all()

    async def call(self, method, params):
        async with self._sem:
            if is_state_mutation(method):
                async with self._state_lock:
                    return await self._do_call(method, params)
            else:
                await self._acquire_read()
                try:
                    return await self._do_call(method, params)
                finally:
                    await self._release_read()
```

### TypeScript/JavaScript

```typescript
// 使用 promise-based 信号量
class LspClient {
    private stateLock = new RWLock();  // 需实现或使用 async-mutex 库
    private sem: Semaphore;

    async call(method: string, params: any): Promise<any> {
        await this.sem.acquire();
        try {
            if (isStateMutation(method)) {
                await this.stateLock.writeLock();
            } else {
                await this.stateLock.readLock();
            }
            try {
                return await this.doSend(method, params);
            } finally {
                if (isStateMutation(method)) {
                    this.stateLock.writeUnlock();
                } else {
                    this.stateLock.readUnlock();
                }
            }
        } finally {
            this.sem.release();
        }
    }
}
```

---

## 五、验证清单

移植完成后，用以下 checklist 确认正确性：

- [ ] `isStateMutation` 覆盖了所有状态变更方法（didOpen/didChange/didClose/didSave）
- [ ] Call 中先获取 sem，再获取 stateMu（顺序不可反！否则死锁）
- [ ] Notify 中状态变更获取写锁，非状态变更不获取锁
- [ ] 读锁和写锁都使用 defer 释放，避免 panic 时泄漏
- [ ] 原有 writeMu（stdin 写入串行化）保持不变
- [ ] 并发测试：N 个查询同时运行，验证 0 错误 0 空结果
- [ ] 混合测试：didOpen + 查询交错，验证不崩溃不报错
- [ ] 信号量测试：sem=1 时查询串行化，sem=N 时最多 N 个并发

## 六、环境变量配置建议

```
LSP_MAX_CONCURRENT_REQUESTS=0  →  无限并发（最激进，仅 stateMu 保护）
LSP_MAX_CONCURRENT_REQUESTS=1  →  全串行（最安全，性能最低）
LSP_MAX_CONCURRENT_REQUESTS=3  →  推荐默认值
LSP_MAX_CONCURRENT_REQUESTS=8  →  高性能环境
```

**选择指南**：
- 新部署先跑 `sem=1` 和 `sem=3` 的对比测试
- 如果两者结果一致 → 用 `sem=3`（查询是线程安全的）
- 如果 sem=3 出现空结果 → 降至 `sem=1`（该 LSP 版本查询也不线程安全）
