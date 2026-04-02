# MCP Language Server

## 项目概述

这是一个 **MCP (Model Context Protocol) 服务器**，用于向 LLM 暴露 Language Server Protocol (LSP) 功能。它作为桥接层，让 MCP 客户端（如 Claude Desktop）能够通过语义工具（定义查找、引用查找、重命名、悬停信息等）导航代码库。

**Go 版本**: 1.24.0

---

## 技术栈

### Go 标准库

| 包 | 用途 |
|----|------|
| `context` | 上下文管理、取消信号 |
| `flag` | 命令行参数解析 |
| `os/exec` | 启动和管理 LSP 服务器子进程 |
| `os/signal` | SIGINT/SIGTERM 信号处理 |
| `bufio` | 缓冲 I/O 读写 |
| `encoding/json` | JSON-RPC 2.0 消息编解码 |
| `path/filepath` | 文件路径处理 |
| `sync/atomic` | 原子操作（请求 ID 计数器） |

### 第三方依赖

| 依赖 | 版本 | 用途 |
|------|------|------|
| `github.com/mark3labs/mcp-go` | v0.25.0 | **MCP 协议通信核心** |
| `github.com/fsnotify/fsnotify` | v1.9.0 | 文件系统监视 |
| `github.com/sabhiram/go-gitignore` | - | .gitignore 模式匹配 |
| `github.com/davecgh/go-spew` | v1.1.1 | 调试输出 |
| `github.com/stretchr/testify` | v1.10.0 | 测试框架（断言） |
| `golang.org/x/text` | v0.25.0 | 文本处理（Unicode 等） |

### LSP 协议来源

- **协议类型**: 从 [vscode-languageserver-node](https://github.com/microsoft/vscode-languageserver-node) (release/protocol/3.17.6-next.9) 自动生成
- **通信处理**: 参考 [gopls](https://github.com/golang/tools/tree/master/gopls) (Go 语言服务器) 的实现
- **代码生成器**: 位于 `cmd/generate/`

---

## 核心模块架构

```
mcp-language-server/
├── main.go                    # 入口点：参数解析、初始化、服务循环
├── tools.go                   # MCP 工具注册和路由
│
├── cmd/generate/              # LSP 协议代码生成器
│   ├── main.go                # 生成入口
│   ├── types.go               # 类型定义
│   ├── tables.go              # 查找表
│   ├── typenames.go           # 类型名
│   ├── methods.go             # 生成的方法
│   └── output.go              # 代码输出
│
├── internal/
│   ├── lsp/                   # LSP 客户端实现
│   │   ├── client.go          # 核心：进程管理、消息分发
│   │   ├── protocol.go        # 协议类型
│   │   ├── methods.go         # LSP 请求封装 (Call/Notify)
│   │   ├── transport.go       # Content-Length 消息协议
│   │   ├── server-request-handlers.go  # 服务端请求处理
│   │   ├── detect-language.go          # 语言检测
│   │   └── typescript.go      # TypeScript 特殊初始化
│   │
│   ├── protocol/              # LSP 协议类型（生成）
│   │   ├── tsprotocol.go      # 主协议类型定义
│   │   ├── tsjson.go          # JSON 序列化/反序列化
│   │   ├── interfaces.go      # 接口定义
│   │   ├── uri.go             # URI 处理
│   │   └── tables.go          # 查找表
│   │
│   ├── tools/                # MCP 工具实现
│   │   ├── definition.go     # 符号定义查找
│   │   ├── references.go      # 引用查找
│   │   ├── diagnostics.go     # 诊断信息
│   │   ├── hover.go           # 悬停信息
│   │   ├── rename-symbol.go   # 重命名符号
│   │   ├── edit_file.go       # 文本编辑
│   │   ├── get-codelens.go    # CodeLens 获取
│   │   ├── execute-codelens.go # CodeLens 执行
│   │   └── utilities.go       # 工具函数
│   │
│   ├── watcher/             # 文件系统监视器
│   │   ├── watcher.go        # 核心逻辑 (fsnotify)
│   │   ├── gitignore.go      # gitignore 匹配
│   │   └── interfaces.go      # LSPClient 接口
│   │
│   ├── utilities/           # 通用工具
│   │   └── edit.go          # WorkspaceEdit 应用
│   │
│   └── logging/            # 日志系统
│       └── logger.go       # 组件化日志
│
└── integrationtests/       # 集成测试
    ├── tests/              # 测试用例 (Go/Rust/Python/TypeScript/Clangd)
    ├── workspaces/         # 模拟工作区
    └── snapshots/         # 快照测试数据
```

---

## MCP 工具列表

| 工具 | 功能 |
|------|------|
| `definition` | 查找符号定义位置 |
| `references` | 查找符号所有引用 |
| `diagnostics` | 获取文件诊断信息（错误/警告） |
| `hover` | 获取悬停信息（类型、文档） |
| `rename_symbol` | 重命名符号 |
| `edit_file` | 多重文本编辑 |

---

## LSP 协议实现

### 消息协议

LSP 使用 HTTP 风格的 Content-Length 协议：

```
Content-Length: <bytes>\r\n\r\n<json>
```

JSON-RPC 2.0 格式：

```json
{"jsonrpc": "2.0", "id": 1, "method": "textDocument/definition", "params": {...}}
```

### 核心通信方法

- `Call(ctx, method, params, result)` - 发送请求并等待响应
- `Notify(ctx, method, params)` - 发送通知（无响应）

### 支持的 LSP 方法

**文档同步**:
- `textDocument/didOpen` / `didChange` / `didClose` / `didSave`

**语义操作**:
- `textDocument/definition` / `references` / `hover` / `rename`
- `textDocument/completion` / `signatureHelp` / `codeAction`
- `textDocument/documentSymbol` / `workspaceSymbol`

**高级特性**:
- `textDocument/semanticTokens` / `callHierarchy` / `typeHierarchy`
- `textDocument/diagnostic`

---

## 文件监视器

使用 `fsnotify` 监视文件变化：

- **防抖**: 300ms debounce 减少 LSP 服务器负载
- **排除目录**: `.git`, `node_modules`, `dist`, `build` 等
- **排除文件**: `.swp`, `.tmp`, `.lock`, 二进制文件等
- **大小限制**: 最大 5MB 文件
- **Gitignore 支持**: 自动排除匹配的文件

---

## 启动流程

```
1. parseConfig()       - 解析 --workspace, --lsp 参数
2. newServer()         - 创建 MCP 服务器
3. initializeLSP()     - 启动 LSP 子进程并初始化
   3.1 创建 LSP 客户端 (lsp.NewClient)
   3.2 发送 initialize 请求
   3.3 注册处理器 (publishDiagnostics 等)
   3.4 启动文件监视器 (WatchWorkspace)
4. registerTools()      - 注册 MCP 工具
5. ServeStdio()        - 进入 MCP 服务循环 (stdio)
6. 优雅关闭            - 关闭文件、shutdown、exit
```

---

## 测试策略

**单元测试**:
- `internal/logging/logger_test.go`
- `internal/watcher/testing/`
- `internal/tools/utilities_test.go`

**集成测试** (`integrationtests/`):
- 使用真实语言服务器：gopls, rust-analyzer, pyright, typescript-language-server, clangd
- 快照测试：`UPDATE_SNAPSHOTS=true go test ./integrationtests/...`

---

## 命令行用法

```bash
mcp-language-server --workspace /path/to/project --lsp gopls
```

**环境变量**:
- `LOG_LEVEL=DEBUG` - 详细日志
- `LOG_FILE=/path/to/log` - 日志文件
- `LSP_CONTEXT_LINES=5` - 上下文行数

**Claude Desktop 配置示例**:

```json
{
  "mcpServers": {
    "language-server": {
      "command": "mcp-language-server",
      "args": ["--workspace", "/path/to/project", "--lsp", "gopls"]
    }
  }
}
```

---

## 架构设计亮点

1. **进程分离**: LSP 服务器作为子进程，通过 stdio 通信
2. **代码生成**: LSP 协议类型自动从 vscode-languageserver-node 生成
3. **优雅关闭**: 多层关闭机制（信号、父进程监控、超时）
4. **内存缓存**: 诊断结果和打开文件状态缓存
5. **Debounce**: 文件变化通知防抖
6. **Gitignore 支持**: 自动排除不需要监视的文件
