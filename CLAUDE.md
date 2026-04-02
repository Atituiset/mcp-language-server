# MCP Language Server

## 项目概述

这是一个 **MCP (Model Context Protocol) 服务器**，用于向 LLM 暴露代码上下文提取能力。本工具是 **LLM 辅助代码分析的上下文提取器**，而非直接的分析工具。它帮助 LLM 获取代码的语义信息（定义、引用、调用关系、结构信息等），由 LLM 进行最终的安全检视和分析。

**典型用途**：百万级 C/C++ 代码库的安全漏洞检视

**工作流**：
```
代码库 → 本工具提取上下文 → LLM 分析 → 安全报告
```

**Go 版本**: 1.24.0

---

## 系统架构总览

```
┌─────────────────────────────────────────────────────────────────────────┐
│                         MCP Language Server                             │
├─────────────────────────────────────────────────────────────────────────┤
│                                                                          │
│  ┌──────────────┐    ┌─────────────────┐    ┌──────────────────┐    │
│  │   main.go    │───>│   mcpServer     │───>│    tools.go       │    │
│  │   入口点     │    │   (编排器)       │    │  (工具注册)       │    │
│  └──────────────┘    └────────┬────────┘    └──────┬───────────┘    │
│                                │                     │                  │
│                                v                     v                  │
│                    ┌─────────────────┐    ┌──────────────────┐      │
│                    │   LSP Client    │    │     Router       │      │
│                    │ internal/lsp/   │    │   (搜索仲裁)     │      │
│                    └────────┬────────┘    └──────┬───────────┘      │
│                             │                     │                    │
│                             v         ┌─────────┴─────────┐          │
│                    ┌─────────────────┐ │   三层搜索架构    │          │
│                    │   文件监视器     │ │                  │          │
│                    │ internal/watcher│ │  L1: ripgrep    │          │
│                    │   (fsnotify)    │ │  L2: tree-sitter│          │
│                    └─────────────────┘ │  L3: LSP        │          │
│                                         └─────────────────┘          │
└─────────────────────────────────────────────────────────────────────────┘
                                    │
                                    v
                        ┌─────────────────────────┐
                        │    外部 LSP 服务器进程   │
                        │  (gopls/clangd/etc.)   │
                        └─────────────────────────┘
```

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
│   │   ├── client.go         # 核心：进程管理、消息分发
│   │   ├── protocol.go       # 协议类型
│   │   ├── methods.go        # LSP 请求封装 (Call/Notify)
│   │   ├── transport.go      # Content-Length 消息协议
│   │   ├── server-request-handlers.go  # 服务端请求处理
│   │   ├── detect-language.go          # 语言检测
│   │   └── typescript.go     # TypeScript 特殊初始化
│   │
│   ├── protocol/              # LSP 协议类型（生成）
│   │   ├── tsprotocol.go     # 主协议类型定义
│   │   ├── tsjson.go        # JSON 序列化/反序列化
│   │   ├── interfaces.go     # 接口定义
│   │   ├── uri.go           # URI 处理
│   │   └── tables.go        # 查找表
│   │
│   ├── tools/                # MCP 工具实现
│   │   ├── definition.go    # 符号定义查找 (L3)
│   │   ├── references.go     # 引用查找 (L3)
│   │   ├── diagnostics.go    # 诊断信息
│   │   ├── hover.go         # 悬停信息
│   │   ├── rename-symbol.go  # 重命名符号
│   │   ├── edit_file.go     # 文本编辑
│   │   ├── get-codelens.go   # CodeLens 获取
│   │   ├── execute-codelens.go # CodeLens 执行
│   │   ├── ripgrep.go       # 文本搜索 (L1)
│   │   ├── treesitter.go     # Tree-sitter 包装 (L2)
│   │   ├── call_hierarchy.go # 调用层级查询 (L3)
│   │   ├── find_struct_usage.go # 结构体使用查询 (L2)
│   │   │
│   │   ├── router/          # 搜索路由
│   │   │   └── router.go   # 统一搜索路由器 + 缓存
│   │   │
│   │   ├── cache/          # 缓存层
│   │   │   └── cache.go   # 线程安全内存缓存
│   │   │
│   │   └── treesitter/      # Tree-sitter 核心
│   │       ├── parser.go    # 解析器 (C/C++)
│   │       ├── query.go     # CSP 查询执行
│   │       ├── ast.go       # AST 工具函数
│   │       └── cursor.go    # TreeCursor 工具
│   │
│   ├── watcher/             # 文件系统监视器
│   │   ├── watcher.go       # 核心逻辑 (fsnotify)
│   │   ├── gitignore.go     # gitignore 匹配
│   │   └── interfaces.go    # LSPClient 接口
│   │
│   ├── utilities/            # 通用工具
│   │   └── edit.go         # WorkspaceEdit 应用
│   │
│   └── logging/              # 日志系统
│       └── logger.go        # 组件化日志
│
└── integrationtests/         # 集成测试
    ├── tests/               # 测试用例 (Go/C/Rust/Python/TypeScript/Clangd)
    ├── workspaces/          # 模拟工作区
    └── snapshots/          # 快照测试数据
```

---

## 分层搜索架构

本项目采用三层搜索架构，平衡速度和语义深度：

```
┌─────────────────────────────────────────────────────────────┐
│                    统一搜索入口 (search)                       │
│              模型自主选择 或 自动智能路由                         │
└─────────────────────┬─────────────────────────────────────┘
                      │
          ┌───────────┼───────────┐
          │           │           │
    ┌─────▼─────┐ ┌───▼────┐ ┌───▼──────┐
    │    L1     │ │   L2   │ │    L3    │
    │  ripgrep  │ │tree-   │ │   LSP    │
    │  (文本)   │ │sitter  │ │ (符号)   │
    │   ⚡⚡⚡   │ │ (AST)  │ │   🐢    │
    │           │ │  ⚡⚡   │ │         │
    └───────────┘ └────────┘ └──────────┘
```

### 各层说明

| 层级 | 工具 | 速度 | 用途 |
|------|------|------|------|
| **L1** | `ripgrep` / `search_text` | ⚡⚡⚡ 最快 | 文本/正则搜索、TODO、注释 |
| **L2** | `treesitter_query` / `search_ast` / `find_struct_usage` | ⚡⚡ 快 | AST 结构查询、CSP 模式、结构体分析 |
| **L3** | `definition` / `references` / `callers` / `callees` / `search_symbol` | 🐢 较慢 | 语义理解、符号定义、调用层级 |

### 路由策略

```
search(query, strategy="auto", intent="...")

Auto 路由规则:
├── intent 包含 "todo"/"comment"/"string" → L1 text
├── intent 包含 "function"/"struct"/"class" → L2 ast
├── intent 包含 "definition"/"reference"/"type" → L3 symbol
└── 无 intent → 并行搜索所有层
```

---

## MCP 工具列表

### 统一搜索（推荐）

| 工具 | 功能 | 层级 |
|------|------|------|
| `search` | 统一搜索入口，支持自动路由 | L1/L2/L3 |
| `search_text` | 强制 L1 文本搜索 | L1 |
| `search_ast` | 强制 L2 AST 查询 | L2 |
| `search_symbol` | 强制 L3 符号搜索 | L3 |

### L1: 文本搜索

| 工具 | 功能 |
|------|------|
| `ripgrep` | 快速文本/正则搜索 |

### L2: AST 查询

| 工具 | 功能 |
|------|------|
| `treesitter_query` | CSP 模式查询 AST |
| `treesitter_ast` | 查看文件 AST 结构 |
| `find_struct_usage` | 查找结构体使用位置 |
| `find_struct_definition` | 查找结构体定义 |

### L3: LSP 语义

| 工具 | 功能 |
|------|------|
| `definition` | 查找符号定义位置 |
| `references` | 查找符号所有引用 |
| `callers` | 查找调用当前函数的函数（支持 depth） |
| `callees` | 查找当前函数调用的函数（支持 depth） |
| `diagnostics` | 获取文件诊断信息 |
| `hover` | 获取悬停信息 |
| `rename_symbol` | 重命名符号 |

### 其他

| 工具 | 功能 |
|------|------|
| `edit_file` | 多重文本编辑 |

---

## 组件交互与数据流

### 启动流程

```
main()
  │
  ├── parseConfig()           # 解析 --workspace, --lsp 参数
  │
  ├── newServer()            # 创建 mcpServer
  │
  └── server.start()
        │
        ├── initializeLSP()
        │     │
        │     ├── lsp.NewClient()          # 启动 LSP 服务器进程
        │     ├── client.InitializeLSPClient()  # 发送 initialize 请求
        │     ├── watcher.NewWorkspaceWatcher(client)
        │     └── watcher.WatchWorkspace()  # 启动文件监视
        │
        ├── server.NewMCPServer()    # 创建 MCP 服务器
        │
        ├── registerTools()          # 注册所有 MCP 工具
        │
        └── server.ServeStdio()     # 阻塞 stdio 循环
```

### 工具执行流程 (以 ripgrep 为例)

```
MCP 客户端
    │
    v
tools.go: s.mcpServer.AddTool(ripgrepTool, handler)
    │
    v
handler 提取参数，调用 tools.SearchCode()
    │
    v
tools/ripgrep.go: 执行 "rg" 命令
    │
    v
返回格式化结果
    │
    v
mcp.NewToolResultText(response)
    │
    v
MCP 客户端接收结果
```

### 搜索流程 (统一 search 工具)

```
MCP 客户端
    │
    v
tools.go: searchRouter.Search(ctx, opts)
    │
    v
router.Search()
    │
    ├──> 检查缓存 (cache.SearchCacheKey)
    │     │
    │     └──> 缓存命中: 返回缓存结果
    │
    └──> 缓存未命中: switch(opts.Strategy)
          │
          ├──> "text"    -> searchText()  -> ripgrep
          ├──> "ast"     -> searchAST()   -> tree-sitter
          ├──> "symbol"  -> searchSymbol()-> ripgrep (后备)
          └──> "auto"    -> routeByIntent() 或 searchAll()
    │
    v
缓存结果（除非出错）
    │
    v
返回 []SearchResult
```

### 文件监视流程

```
文件系统事件 (创建/写入/删除)
    │
    v
fsnotify 检测到事件
    │
    v
WorkspaceWatcher.event loop 接收事件
    │
    v
shouldExcludeFile/shouldExcludeDir() 检查
    │
    v
isPathWatched() - 匹配 LSP 注册
    │
    v
debounceHandleFileEvent() - 合并快速事件
    │
    v
handleFileEvent()
    │
    ├──> 如果文件已打开且变更 -> client.NotifyChange()
    │
    └──> 否则 -> client.DidChangeWatchedFiles() 通知
```

### 关闭流程

```
信号 (SIGINT/SIGTERM) 或 父进程死亡
    │
    v
cleanup()
    │
    ├──> lspClient.CloseAllFiles()  # 发送 didClose
    ├──> lspClient.Shutdown()      # 发送 shutdown (带超时)
    ├──> lspClient.Exit()          # 发送 exit
    ├──> lspClient.Close()         # 关闭进程和管道
    │
    v
退出进程
```

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
- `callHierarchy/incomingCalls` / `callHierarchy/outgoingCalls`

---

## Tree-sitter 支持

### 支持语言

- **C** (`github.com/smacker/go-tree-sitter/c`)
- **C++** (`github.com/smacker/go-tree-sitter/cpp`)

### CSP 查询示例

```scheme
; 查找所有函数定义
(function_definition) @func

; 查找特定名称的函数
((identifier) @name (#eq? @name "main"))

; 查找结构体定义
(struct_specifier) @struct

; 查找类型使用
(type_identifier) @type
```

---

## 缓存机制

### 架构

```
Search(query)
    │
    ├──> cache.Get(cacheKey) ──────> 命中 → 返回缓存
    │
    └──> 未命中
          │
          ├──> 执行搜索
          │
          └──> cache.Set(cacheKey, results) → 返回
```

### 特性

- **线程安全**: 使用 `sync.RWMutex`
- **TTL 过期**: 默认 5 分钟，可配置
- **缓存键**: 基于 query/strategy/filePath/language 生成

### 缓存键生成

```go
func SearchCacheKey(query, strategy, filePath, language string) string {
    return fmt.Sprintf("%s:%s:%s:%s:", query, strategy, filePath, language)
}
```

---

## 文件监视器

使用 `fsnotify` 监视文件变化：

- **防抖**: 300ms debounce 减少 LSP 服务器负载
- **排除目录**: `.git`, `node_modules`, `dist`, `build` 等
- **排除文件**: `.swp`, `.tmp`, `.lock`, 二进制文件等
- **大小限制**: 最大 5MB 文件
- **Gitignore 支持**: 自动排除匹配的文件

### 排除规则

1. `.gitignore` 中的模式
2. 以 `.` 开头的目录/文件
3. 配置中指定的排除目录
4. 二进制文件
5. 超过大小限制的文件

---

## 典型用法：LLM 辅助安全检视

```
用户: "检查 main.cpp 中的 getUserInput 函数是否有安全漏洞"

工具链:
1. definition("getUserInput")           → 获取函数定义和实现
2. callers("main.cpp", line, col, depth=3)  → 提取上游调用链
3. callees("main.cpp", line, col, depth=2)   → 提取下游函数调用
4. find_struct_usage("UserData")       → 查找相关结构体使用

组合上下文 → 发送给 LLM → LLM 输出安全分析报告
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

1. **上下文提取定位**: 本工具专注提取代码上下文（定义、引用、调用关系、AST 结构），由 LLM 负责安全分析
2. **三层搜索架构**: ripgrep (L1) + tree-sitter (L2) + LSP (L3)，平衡速度与语义
3. **智能路由**: 统一入口，自动选择最佳搜索层
4. **结果缓存**: 搜索结果缓存，减少重复查询
5. **进程分离**: LSP 服务器作为子进程，通过 stdio 通信
6. **代码生成**: LSP 协议类型自动从 vscode-languageserver-node 生成
7. **优雅关闭**: 多层关闭机制（信号、父进程监控、超时）
8. **内存缓存**: 诊断结果和打开文件状态缓存
9. **Debounce**: 文件变化通知防抖
10. **Gitignore 支持**: 自动排除不需要监视的文件
