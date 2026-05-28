# MCP Language Server 架构深度分析与使用指南

> 生成日期: 2026-05-28
> 基于源码逐文件分析（含 GPT5.5 重构后最新代码）

---

## 目录

1. [项目定位与核心概念](#1-项目定位与核心概念)
2. [整体架构](#2-整体架构)
3. [核心模块详解](#3-核心模块详解)
   - 3.1 [入口与生命周期 (main.go)](#31-入口与生命周期-maingo)
   - 3.2 [MCP 工具注册层 (tools.go)](#32-mcp-工具注册层-toolsgo)
   - 3.3 [LSP 客户端 (internal/lsp/)](#33-lsp-客户端-internallsp)
   - 3.4 [三层搜索路由器 (internal/tools/router/)](#34-三层搜索路由器-internaltoolsrouter)
   - 3.5 [工具实现层 (internal/tools/)](#35-工具实现层-internaltools)
   - 3.6 [Tree-sitter 核心 (internal/tools/treesitter/)](#36-tree-sitter-核心-internaltoolstreesitter)
   - 3.7 [文件监视器 (internal/watcher/)](#37-文件监视器-internalwatcher)
   - 3.8 [缓存系统 (internal/tools/cache/)](#38-缓存系统-internaltoolscache)
   - 3.9 [日志系统 (internal/logging/)](#39-日志系统-internallogging)
   - 3.10 [工具函数 (internal/utilities/)](#310-工具函数-internalutilities)
   - 3.11 [LSP 协议代码生成 (cmd/generate/)](#311-lsp-协议代码生成-cmdgenerate)
   - 3.12 [符号解析器 (symbol_resolver.go)](#312-符号解析器-symbol_resolvergo)
   - 3.13 [MCP App UI 资源 (ui_resources.go)](#313-mcp-app-ui-资源-ui_resourcesgo)
4. [数据流分析](#4-数据流分析)
5. [设计亮点与已知局限](#5-设计亮点与已知局限)
6. [开发环境使用](#6-开发环境使用)
7. [部署环境使用](#7-部署环境使用)
8. [三层搜索架构 vs 纯 LSP：价值分析](#8-三层搜索架构-vs-纯-lsp价值分析)

---

## 1. 项目定位与核心概念

**MCP Language Server** 是一个 MCP (Model Context Protocol) 服务器，核心定位是 **LLM 的代码上下文提取器**。

它不直接做代码分析或安全检视，而是将代码的语义信息提取出来喂给 LLM：

```
代码库 → 本工具提取上下文 → LLM 分析 → 安全报告/代码理解
```

**典型用途**：百万级 C/C++ 代码库的安全漏洞检视。

**核心设计哲学**：

- **上下文提取，不做分析** — 工具只负责"取"，LLM 负责"想"
- **三层搜索架构** — 速度与语义深度的平衡：ripgrep (L1) + tree-sitter (L2) + LSP (L3)
- **进程隔离** — LSP 服务器作为子进程，崩溃不影响主服务
- **MCP stdio 协议** — 与 MCP 客户端（如 Claude Desktop）通过 stdin/stdout 通信
- **数据/格式分离** — 工具实现拆分为数据获取（GetXxxData）和格式化（FormatXxxData），支持结构化输出和 MCP App UI 渲染

**技术栈**：

| 组件 | 技术 |
|------|------|
| 语言 | Go 1.24.0 |
| MCP SDK | mark3labs/mcp-go v0.25.0 |
| LSP 协议 | 自动生成的类型和方法 |
| Tree-sitter | smacker/go-tree-sitter (C/C++) |
| 文件监视 | fsnotify |
| Gitignore | sabhiram/go-gitignore |
| 文本搜索 | ripgrep (外部命令) |

---

## 2. 整体架构

```
MCP 客户端 (Claude Desktop / IDE / CLI)
│
│ stdio (MCP JSON-RPC)
▼
┌─────────────────────────────────────────────────────────────────┐
│ main.go → mcpServer (全局编排器) │
│ │
│ ┌──────────────┐ ┌─────────────────┐ ┌──────────────────┐ │
│ │ MCP Server │──>│ tools.go │──>│ 工具实现函数 │ │
│ │ (mcp-go) │ │ (工具注册/路由) │ │ (tools/*.go) │ │
│ └──────────────┘ └────────┬────────┘ └────────┬─────────┘ │
│ │ │ │
│ ▼ ▼ │
│ ┌────────────────┐ ┌──────────────────┐ │
│ │ Router │ │ LSP Client │ │
│ │ (三层搜索路由) │ │ (internal/lsp/) │ │
│ │ + 缓存 │ │ │ │
│ └───────┬────────┘ └────────┬─────────┘ │
│ │ │ │
│ ┌──────────────────┐ │ ┌────────┴─────────┐ │
│ │ Watcher │◄─────┤ │ 文档生命周期管理 │ │
│ │ (internal/watcher)│ │ │ 诊断缓存 │ │
│ │ fsnotify │ │ │ 消息分发 │ │
│ └──────────────────┘ │ └────────┬─────────┘ │
│ │ │ │
│ ┌──────────────────┐ │ │ │
│ │ Tree-sitter │◄─────┘ │ │
│ │ (C/C++ 解析) │ │ │
│ └──────────────────┘ │ │
│ │ │ │
│ ┌──────────────────┐ │ │ │
│ │ Ripgrep │ │ │ │
│ │ (外部命令) │ │ │ │
│ └──────────────────┘ │ │
│ │ │ │
│ ┌──────────────────┐ │ │ │
│ │ UI Resources │◄───┘ │ │
│ │ (ui_resources.go)│ │ │
│ │ MCP App HTML │ │ │
│ └──────────────────┘ │ │
└───────────────────────────────────────────────────┼────────────┘
│ stdin/stdout pipe
▼
┌──────────────────┐
│ 外部 LSP 服务器 │
│ (gopls/clangd/ │
│ pyright/rust- │
│ analyzer/...) │
└──────────────────┘
```

### 目录结构

```
mcp-language-server/
├── main.go # 入口点：参数解析、初始化、服务循环、优雅关闭
├── tools.go # MCP 工具注册和路由（17+ 工具定义 + 辅助函数）
├── ui_resources.go # MCP App UI 资源（调用层级图 + 诊断仪表盘 HTML）
│
├── cmd/generate/ # LSP 协议代码生成器
│ ├── main.go # 生成入口
│ ├── types.go # 类型定义
│ ├── tables.go # 查找表
│ ├── typenames.go # 类型名
│ ├── methods.go # 生成的方法
│ └── output.go # 代码输出
│
├── internal/
│ ├── lsp/ # LSP 客户端实现
│ │ ├── client.go # 核心：进程管理、文档生命周期、诊断缓存
│ │ ├── protocol.go # JSON-RPC 2.0 消息/ID 类型
│ │ ├── transport.go # Content-Length 帧协议 + 消息分发循环
│ │ ├── methods.go # 自动生成的 LSP 方法封装 (40+)
│ │ ├── server-request-handlers.go # 服务端请求处理
│ │ ├── detect-language.go # 文件扩展名 → LanguageID 映射
│ │ └── typescript.go # TypeScript 服务器特殊初始化
│ │
│ ├── protocol/ # LSP 协议类型（自动生成）
│ │ ├── tsprotocol.go # 主协议类型定义
│ │ ├── tsjson.go # JSON 序列化/反序列化
│ │ ├── interfaces.go # 接口定义（多态类型处理）
│ │ ├── uri.go # URI 处理
│ │ └── tables.go # 查找表（KindMap 等）
│ │
│ ├── tools/ # MCP 工具实现
│ │ ├── definition.go # 符号定义查找 (L3)
│ │ ├── references.go # 引用查找 (L3)
│ │ ├── diagnostics.go # 诊断信息获取（数据/格式分离）
│ │ ├── hover.go # 悬停信息获取
│ │ ├── rename-symbol.go # 重命名符号
│ │ ├── edit_file.go # 多重文本编辑
│ │ ├── ripgrep.go # 文本搜索 (L1)
│ │ ├── treesitter.go # Tree-sitter 查询/AST 封装 (L2)
│ │ ├── call_hierarchy.go # 调用层级查询 (L3)（数据/格式分离）
│ │ ├── find_struct_usage.go # 结构体使用/定义查询 (L2)
│ │ ├── lsp-utilities.go # LSP 共享工具：GetFullDefinition + GetLineRangesToDisplay
│ │ ├── symbol_resolver.go # 符号名→LSP位置 解析器
│ │ ├── logging.go # 组件日志实例 (toolsLogger)
│ │ ├── utilities.go # 通用格式化工具
│ │ ├── get-codelens.go # CodeLens 获取（已注释）
│ │ └── execute-codelens.go # CodeLens 执行（已注释）
│ │
│ │ ├── router/ # 搜索路由
│ │ │ └── router.go # 统一搜索路由器 + 智能路由 + 缓存集成
│ │ │
│ │ ├── cache/ # 缓存层
│ │ │ └── cache.go # 线程安全内存缓存 + 搜索/结构体/调用层级专用缓存
│ │ │
│ │ └── treesitter/ # Tree-sitter 核心
│ │ ├── parser.go # 解析器封装 (C/C++)
│ │ ├── query.go # CSP 查询执行
│ │ ├── ast.go # AST 工具函数
│ │ └── cursor.go # TreeCursor 工具
│ │
│ ├── watcher/ # 文件系统监视器
│ │ ├── watcher.go # 核心逻辑 (fsnotify)
│ │ ├── gitignore.go # Gitignore 匹配
│ │ └── interfaces.go # LSPClient 接口 + WatcherConfig
│ │
│ ├── utilities/ # 通用工具
│ │ └── edit.go # WorkspaceEdit 应用 (文本编辑/文件操作)
│ │
│ └── logging/ # 日志系统
│ └── logger.go # 组件化日志 (6 组件 + 级别控制)
│
├── integrationtests/ # 集成测试
│ ├── tests/ # 测试用例
│ ├── workspaces/ # 模拟工作区 (Go/C/Rust/Python/TypeScript/Clangd)
│ └── snapshots/ # 快照测试数据
│
├── docs/ # 文档
├── justfile # 开发任务脚本
└── go.mod # Go 模块定义
```

---

## 3. 核心模块详解

### 3.1 入口与生命周期 (main.go)

#### mcpServer 结构体

```go
type mcpServer struct {
    config           config                   // 工作区目录 + LSP 命令
    lspClient        *lsp.Client              // LSP 客户端实例
    mcpServer        *server.MCPServer        // MCP 协议层服务器
    ctx              context.Context          // 全局上下文
    cancelFunc       context.CancelFunc       // 取消函数
    workspaceWatcher *watcher.WorkspaceWatcher // 文件监视器
}
```

#### 启动流程 (`main.go:106-127`)

```
main()
│
├── parseConfig() # 解析 --workspace 和 --lsp 参数
├── newServer(config) # 创建 mcpServer 实例
│
└── server.start()
    │
    ├── initializeLSP()
    │   ├── os.Chdir(workspaceDir) # 切换工作目录
    │   ├── lsp.NewClient(command, args...) # 启动 LSP 子进程
    │   ├── watcher.NewWorkspaceWatcher(client) # 创建文件监视器
    │   ├── client.InitializeLSPClient() # 发送 initialize + initialized
    │   │   ├── 注册 ServerRequestHandler (applyEdit, configuration, registerCapability)
    │   │   ├── 注册 NotificationHandler (showMessage, publishDiagnostics)
    │   │   └── TypeScript 特殊初始化
    │   ├── go watcher.WatchWorkspace() # 后台启动文件监视
    │   └── client.WaitForServerReady() # 等待 1 秒（TODO: 改进）
    │
    ├── server.NewMCPServer() # 创建 MCP 服务器
    ├── registerUIResources() # 注册 MCP App UI 资源（调用层级图 + 诊断仪表盘）
    ├── registerTools() # 注册 17+ MCP 工具
    └── server.ServeStdio() # 阻塞进入 stdio 消息循环
```

#### 关闭机制 (`main.go:196-248`)

设计三层保障确保 LSP 子进程正确关闭：

```
关闭触发:
├── SIGINT / SIGTERM 信号
└── 父进程死亡检测 (每 100ms 轮询 os.Getppid())

cleanup() 流程:
├── CloseAllFiles() # 发送 textDocument/didClose 给所有打开的文件
├── Shutdown() (500ms 超时) # 发送 shutdown 请求
│   └── 超时后继续，不阻塞
├── Exit() # 发送 exit 通知
├── Close() # 关闭 stdin pipe + 等待进程退出
│   └── 2 秒后 force kill # 进程不退出则 Kill()
└── close(done) # 通知主 goroutine 退出
```

**父进程死亡检测** 解决了 Claude Desktop 不正确杀子进程的实际问题：每 100ms 检查 `os.Getppid()`，如果 ppid 变为 1（init 进程收养），说明父进程已死。

---

### 3.2 MCP 工具注册层 (tools.go)

`tools.go` 是 MCP 工具的定义和注册中心。GPT5.5 重构后，引入了以下关键模式：

#### 数据/格式分离模式

工具实现拆分为两个阶段，使得 MCP 既可以返回纯文本给 LLM，也可以返回结构化数据给 MCP App UI：

```
1. GetXxxData()  → 获取结构化数据（Go struct）
2. FormatXxxData() → 将结构化数据格式化为文本
3. tools.go handler 中：
   - 纯文本工具: 调 GetXxxData() + FormatXxxData() → mcp.NewToolResultText()
   - UI 工具: 调 GetXxxData() → newAppToolResult(data, FormatXxxData(data), resourceURI)
```

已应用此模式的工具：

| 工具 | 数据函数 | 格式化函数 | UI 资源 |
|------|----------|-----------|---------|
| `diagnostics` | `GetDiagnosticsDataForFile()` | `FormatDiagnosticsData()` | `ui://diagnostics/dashboard` |
| `callers` | `GetCallersData()` | `FormatCallHierarchyData()` | `ui://call-hierarchy/graph` |
| `callees` | `GetCalleesData()` | `FormatCallHierarchyData()` | `ui://call-hierarchy/graph` |

#### 辅助函数 (tools.go 底部)

GPT5.5 提取了三个可复用的辅助函数：

```go
// appToolMeta — 为工具附加 MCP App UI 资源元数据
func appToolMeta(resourceURI string) *mcp.Meta

// newAppToolResult — 同时返回结构化数据和文本回退
func newAppToolResult(structured any, fallbackText, resourceURI string) *mcp.CallToolResult

// readIntArgument — 统一处理 JSON 数值参数（float64/int）
func readIntArgument(args map[string]any, name string) (int, bool)
```

#### symbolName 参数支持

GPT5.5 为 `callers` 和 `callees` 工具新增了 `symbolName` 参数，允许通过符号名而非行号/列号调用：

```
callers/callees 参数解析:
├── 有 filePath + line + column? → 直接使用位置
├── 无位置但有 symbolName? → 调用 ResolveSymbolLocation() 解析位置
│   ├── 成功: 用解析出的 filePath/line/column 继续查询
│   └── 失败: 返回错误
└── 两者都没有? → 返回参数错误
```

#### 注册的全部工具

| 工具名 | 层级 | 参数 | 调用函数 | UI 资源 |
|--------|------|------|----------|---------|
| `edit_file` | - | filePath, edits[] | `tools.ApplyTextEdits()` | - |
| `definition` | L3 | symbolName | `tools.ReadDefinition()` | - |
| `references` | L3 | symbolName | `tools.FindReferences()` | - |
| `diagnostics` | L3 | filePath, contextLines, showLineNumbers | `GetDiagnosticsDataForFile()` + `FormatDiagnosticsData()` | `ui://diagnostics/dashboard` |
| `hover` | L3 | filePath, line, column | `tools.GetHoverInfo()` | - |
| `rename_symbol` | L3 | filePath, line, column, newName | `tools.RenameSymbol()` | - |
| `ripgrep` | L1 | pattern, caseSensitive, wholeWord, maxCount, contextLines, fileType, include | `tools.SearchCode()` | - |
| `treesitter_query` | L2 | query, filePath, language | `tools.RunTreesitterQuery()` | - |
| `treesitter_ast` | L2 | filePath, nodeType, maxDepth | `tools.GetAST()` | - |
| `search` | L1/L2/L3 | query, strategy, intent, filePath, language | `searchRouter.Search()` | - |
| `search_text` | L1 | query, filePath | `searchRouter.Search()` (strategy=text) | - |
| `search_ast` | L2 | query, filePath, language | `searchRouter.Search()` (strategy=ast) | - |
| `search_symbol` | L3 | query, filePath | `searchRouter.Search()` (strategy=symbol) | - |
| `callers` | L3 | symbolName, filePath, line, column, depth | `GetCallersData()` + `FormatCallHierarchyData()` | `ui://call-hierarchy/graph` |
| `callees` | L3 | symbolName, filePath, line, column, depth | `GetCalleesData()` + `FormatCallHierarchyData()` | `ui://call-hierarchy/graph` |
| `find_struct_usage` | L2 | structName, filePath, language | `tools.FindStructUsage()` | - |
| `find_struct_definition` | L2 | structName, filePath, language | `tools.FindStructDefinition()` | - |

（`get_codelens` 和 `execute_codelens` 已注释掉，保留待启用）

#### 统一搜索结果格式化

`search`/`search_text`/`search_ast`/`search_symbol` 共享 `formatSearchResults()` 函数：

```go
func formatSearchResults(results []router.SearchResult) *mcp.CallToolResult {
    // 输出格式:
    // === [text layer] (23 results) ===
    // <内容>
    //
    // === [ast layer] (5 results) ===
    // <内容>
}
```

---

### 3.3 LSP 客户端 (internal/lsp/)

这是项目最复杂的模块，实现了一个功能完整的 LSP 客户端。

#### 进程管理 (client.go:49-102)

```go
func NewClient(command string, args ...string) (*Client, error) {
    cmd := exec.Command(command, args...) // 创建子进程
    cmd.Env = os.Environ() // 继承环境变量

    stdin, _ := cmd.StdinPipe()   // 写入管道
    stdout, _ := cmd.StdoutPipe()  // 读取管道
    stderr, _ := cmd.StderrPipe()  // 错误输出管道

    cmd.Start() // 启动进程

    go handleStderr()       // 后台转发 stderr 到日志
    go client.handleMessages() // 后台消息分发循环
}
```

**Client 核心字段**：

| 字段 | 类型 | 用途 |
|------|------|------|
| `nextID` | `atomic.Int32` | 请求 ID 自增计数器 |
| `handlers` | `map[string]chan *Message` | 等待响应的 channel（key=ID字符串） |
| `serverRequestHandlers` | `map[string]ServerRequestHandler` | 服务器→客户端请求处理 |
| `notificationHandlers` | `map[string]NotificationHandler` | 服务器→客户端通知处理 |
| `diagnostics` | `map[DocumentUri][]Diagnostic` | 诊断信息缓存 |
| `openFiles` | `map[string]*OpenFileInfo` | 已打开文件跟踪（URI → version） |

#### 消息协议 (transport.go)

LSP 使用 HTTP 风格的 **Content-Length** 帧协议：

```
发送:
Content-Length: <字节数>\r\n\r\n<JSON>

接收:
1. 逐行读 header，解析 Content-Length
2. 读取指定长度的 body
3. JSON 反序列化为 Message
```

**Message 结构** (`protocol.go`):

```go
type Message struct {
    JSONRPC string          `json:"jsonrpc"`           // 固定 "2.0"
    ID      *MessageID      `json:"id,omitempty"`      // 请求/响应 ID (int32 或 string)
    Method  string          `json:"method,omitempty"`  // 方法名
    Params  json.RawMessage `json:"params,omitempty"`  // 参数
    Result  json.RawMessage `json:"result,omitempty"`  // 响应结果
    Error   *ResponseError  `json:"error,omitempty"`   // 错误
}
```

`MessageID` 支持 int32 和 string 两种类型（JSON-RPC 规范允许），自定义了 `MarshalJSON`/`UnmarshalJSON` 处理 float64 → int32 的转换。

#### 消息分发循环 (transport.go:98-192)

`handleMessages()` 是一个永久循环，根据消息特征分为三类：

```
收到消息
│
├── 有 Method + 有 ID → Server→Client 请求
│   ├── 查找 serverRequestHandlers[method]
│   ├── 执行 handler → 得到 result/error
│   ├── 构造响应 Message (相同 ID)
│   └── WriteMessage 发回服务器
│
├── 有 Method + 无 ID → Server→Client 通知
│   ├── 查找 notificationHandlers[method]
│   └── go handler(params) // 异步执行
│
└── 无 Method + 有 ID → Client 请求的响应
    ├── 转换 ID 为字符串
    ├── 查找 handlers[idStr] channel
    ├── ch <- msg // 发送到 channel
    └── close(ch) // 关闭 channel
```

#### Call/Notify 机制 (transport.go:194-266)

**Call (请求-响应)**：

```go
func (c *Client) Call(ctx, method, params, result) error {
    id := c.nextID.Add(1)              // 原子自增 ID
    msg := NewRequest(id, method, params)

    ch := make(chan *Message, 1)       // 创建响应 channel
    c.handlers[idStr] = ch             // 注册到等待表
    defer delete(c.handlers, idStr)    // 清理

    WriteMessage(c.stdin, msg)         // 发送请求
    resp := <-ch                       // 阻塞等待响应

    // 处理错误或反序列化结果
    json.Unmarshal(resp.Result, result)
}
```

**Notify (单向通知)**：

```go
func (c *Client) Notify(ctx, method, params) error {
    msg := NewNotification(method, params) // 无 ID
    WriteMessage(c.stdin, msg)             // 发送，不等响应
}
```

#### 文档生命周期 (client.go)

LSP 协议要求客户端管理文档的打开/修改/关闭状态：

```
OpenFile(path)
├── 检查 openFiles map，已打开则跳过
├── os.ReadFile(path) 读取内容
├── Notify("textDocument/didOpen", TextDocumentItem{URI, LanguageID, Version:1, Text})
└── openFiles[uri] = &OpenFileInfo{Version: 1}

NotifyChange(path)
├── 检查文件必须在 openFiles 中
├── os.ReadFile(path) 读取新内容
├── fileInfo.Version++ 递增版本号
├── Notify("textDocument/didChange", {Version, ContentChanges:[{Text}]})
└── 使用全量同步模式 (TextDocumentSyncKindFull)

CloseFile(path)
├── 检查 openFiles map
├── Notify("textDocument/didClose", {URI})
└── delete(openFiles, uri)
```

**重要细节**：文件同步使用**全量内容模式**（每次变更发送整个文件内容），而非增量模式。这对大文件性能不佳，但实现简单。

#### 诊断缓存 (client.go)

LSP 服务器通过 `textDocument/publishDiagnostics` 通知推送诊断信息，客户端将其缓存：

```go
func HandleDiagnostics(client *Client, params json.RawMessage) {
    var diagParams protocol.PublishDiagnosticsParams
    json.Unmarshal(params, &diagParams)

    client.diagnosticsMu.Lock()
    client.diagnostics[diagParams.URI] = diagParams.Diagnostics
    client.diagnosticsMu.Unlock()
}
```

`GetFileDiagnostics(uri)` 从缓存读取，供 `diagnostics` 工具使用。

#### 服务端请求处理 (server-request-handlers.go)

处理 LSP 服务器主动发起的请求和通知：

| 方法 | 类型 | 处理逻辑 |
|------|------|----------|
| `workspace/applyEdit` | 请求 | 调用 `utilities.ApplyWorkspaceEdit()` 应用编辑 |
| `workspace/configuration` | 请求 | 返回空配置 `[]map[string]any{{}}` |
| `client/registerCapability` | 请求 | 解析注册选项，文件监视器注册转发给 Watcher |
| `window/showMessage` | 通知 | 按严重程度记录日志 |
| `textDocument/publishDiagnostics` | 通知 | 缓存到 diagnostics map |

**文件监视器注册转发**：当 LSP 服务器注册 `workspace/didChangeWatchedFiles` 能力时，解析出 `FileSystemWatcher` 列表，通过全局 `fileWatchHandler` 回调传给 Watcher。

#### 初始化流程 (client.go)

```
InitializeLSPClient(ctx, workspaceDir)
│
├── Call("initialize", InitializeParams)
│   ├── WorkspaceFolders: [{URI, Name}]
│   ├── ProcessID, ClientInfo
│   ├── RootPath, RootURI
│   ├── Capabilities: (详尽的客户端能力声明)
│   │   ├── Workspace: configuration, didChangeConfiguration, didChangeWatchedFiles
│   │   ├── TextDocument: synchronization, completion, codeLens, codeAction,
│   │   │   publishDiagnostics, semanticTokens
│   │   └── Window
│   └── InitializationOptions: (gopls codelenses 配置)
│
├── Notify("initialized", {}) # 通知初始化完成
│
├── 注册 ServerRequestHandler:
│   ├── "workspace/applyEdit" → HandleApplyEdit
│   ├── "workspace/configuration" → HandleWorkspaceConfiguration
│   └── "client/registerCapability" → HandleRegisterCapability
│
├── 注册 NotificationHandler:
│   ├── "window/showMessage" → HandleServerMessage
│   └── "textDocument/publishDiagnostics" → HandleDiagnostics(c)
│
├── Notify("initialized", InitializedParams{}) # 二次初始化
│
└── TypeScript 特殊初始化 (如果是 typescript-language-server)
```

#### LSP 方法封装 (methods.go)

**自动生成**的 40+ LSP 方法，每个是对 `Call()`/`Notify()` 的类型安全封装：

```go
// 请求方法示例
func (c *Client) Definition(ctx, params) (Or_Result_textDocument_definition, error) {
    var result protocol.Or_Result_textDocument_definition
    err := c.Call(ctx, "textDocument/definition", params, &result)
    return result, err
}

// 通知方法示例
func (c *Client) DidOpen(ctx, params) error {
    return c.Notify(ctx, "textDocument/didOpen", params)
}

// 无响应请求示例
func (c *Client) Shutdown(ctx) error {
    return c.Call(ctx, "shutdown", nil, nil)
}
```

覆盖的方法包括：Initialize, Shutdown, Definition, References, Hover, Completion, Rename, CodeAction, DocumentSymbol, Symbol, PrepareCallHierarchy, IncomingCalls, OutgoingCalls, SemanticTokens, Diagnostic, Formatting 等。

---

### 3.4 三层搜索路由器 (internal/tools/router/)

Router 是统一搜索入口，实现三层搜索架构的智能路由。

#### Router 结构

```go
type Router struct {
    workspaceDir string                 // 工作区目录
    cache        *cache.SearchResultCache // 搜索结果缓存
}
```

#### SearchOptions

```go
type SearchOptions struct {
    Query    string // 搜索查询
    Strategy string // "auto" | "text" | "ast" | "symbol"
    Intent   string // 意图提示: "todo", "function", "definition" 等
    FilePath string // 限定文件路径
    Language string // 语言: "c" | "cpp" | "auto"
}
```

#### 搜索流程

```
Search(ctx, opts)
│
├── 生成缓存键: query:strategy:filePath:language
├── 缓存命中? → 直接返回
│
└── 缓存未命中 → switch(strategy)
    │
    ├── "text" → searchText()
    │   └── tools.SearchCode() → ripgrep 命令
    │
    ├── "ast" → searchAST()
    │   └── tools.RunTreesitterQuery() → tree-sitter CSP 查询
    │
    ├── "symbol" → searchSymbol()
    │   └── tools.SearchCode() → ripgrep (大小写敏感)
    │   ⚠️ 注意: 当前用 ripgrep 作为 LSP symbol 的后备
    │   ⚠️ 这并非真正的 LSP 语义搜索
    │
    └── "auto" → searchAuto()
        │
        ├── 有 intent? → routeByIntent()
        │   ├── "todo/fixme/comment/string/text/pattern/word" → "text"
        │   ├── "function/struct/class/node/ast/syntax/definition/declare" → "ast"
        │   ├── "symbol/reference/call/usage/import/type/variable" → "symbol"
        │   └── 无法匹配 → searchAll()
        │
        └── 无 intent → searchAll()
            │
            ├── goroutine 1: searchText (L1)
            ├── goroutine 2: searchAST (L2)
            ├── goroutine 3: searchSymbol (L3) ⚠️ 实际也是 ripgrep
            └── WaitGroup 等待所有完成
            └── 合并结果，任一层失败不影响其他层
```

#### 智能路由关键词映射

| 目标层 | 关键词 |
|--------|--------|
| text (L1) | todo, fixme, comment, string, text, pattern, word, find text |
| ast (L2) | function, struct, class, node, ast, syntax, definition, declare |
| symbol (L3) | symbol, reference, call, usage, import, type, variable |

---

### 3.5 工具实现层 (internal/tools/)

每个工具是独立文件，通过 MCP 暴露给 LLM。GPT5.5 重构后，引入了数据/格式分离和共享 LSP 工具函数。

#### definition.go — 符号定义查找 (L3)

```
ReadDefinition(ctx, client, symbolName)
│
├── client.Symbol(WorkspaceSymbolParams{Query: symbolName})
│   └── workspace/symbol 请求
│
├── 遍历结果，符号名匹配:
│   ├── 限定名 ("Type.Method"): 精确匹配 symbol.GetName()
│   └── 非限定名 ("Method"):
│       ├── Method 类型: 匹配后缀 "::Method" 或 ".Method" 或精确
│       └── 其他类型: 精确匹配
│
├── 对每个匹配符号:
│   ├── client.OpenFile() 打开文件
│   ├── GetFullDefinition() 获取完整定义代码 (来自 lsp-utilities.go)
│   └── addLineNumbers() 添加行号
│
└── 拼接所有定义:
    ---
    Symbol: MyFunction
    File: /path/to/file.go
    Kind: Function
    Range: L42:C1 - L55:C1

    42: func MyFunction() {
    43: ...
```

**GPT5.5 变化**：日志从 `fmt` 直接输出改为使用 `toolsLogger.Debug/Error`（来自 `logging.go`），调用 `GetFullDefinition` 改为使用 `lsp-utilities.go` 中的共享版本。

#### references.go — 引用查找 (L3)

```
FindReferences(ctx, client, symbolName)
│
├── client.Symbol() 查找符号位置
├── 符号名匹配 (与 definition 相同逻辑)
│
├── 对每个符号:
│   ├── client.OpenFile()
│   ├── client.References(ReferenceParams{IncludeDeclaration: false})
│   │
│   └── 按文件分组引用:
│       ├── 排序 URI 列表
│       ├── 读取源文件
│       ├── GetLineRangesToDisplay() 计算带上下文的行范围 (来自 lsp-utilities.go)
│       ├── ConvertLinesToRanges() 转换为连续范围
│       └── FormatLinesWithRanges() 格式化输出
│
└── 输出格式:
    ---
    /path/to/file.go
    References in File: 3
    At: L10:C5, L25:C12, L42:C8

    10: result := MyFunction(arg)
```

**GPT5.5 变化**：使用 `GetLineRangesToDisplay` 从 `lsp-utilities.go` 替代了之前内联的逻辑；日志使用 `toolsLogger`。

#### call_hierarchy.go — 调用链查询 (L3) (GPT5.5 重写)

GPT5.5 将调用层级工具重构为数据/格式分离模式，并引入 `CallHierarchyData` 结构体：

```go
type CallResult struct {
    Name     string `json:"name"`     // 函数名称
    FilePath string `json:"filePath"` // 文件路径
    Line     int    `json:"line"`     // 行号
    Column   int    `json:"column"`   // 列号
    Depth    int    `json:"depth"`    // 调用的深度层级
}

type CallHierarchyData struct {
    Direction string       `json:"direction"`  // "incoming" 或 "outgoing"
    FilePath  string       `json:"filePath"`
    Line      int          `json:"line"`
    Column    int          `json:"column"`
    MaxDepth  int          `json:"maxDepth"`
    Prepared  bool         `json:"prepared"`   // PrepareCallHierarchy 是否成功
    Total     int          `json:"total"`
    Results   []CallResult `json:"results"`
}
```

调用流程：

```
GetCallers(ctx, client, filePath, line, column, depth)
│
├── GetCallersData(ctx, client, filePath, line, column, depth)
│   ├── client.OpenFile()
│   ├── client.PrepareCallHierarchy()
│   ├── 收集初始 CallHierarchyItem
│   ├── seen map 初始化（循环检测）
│   ├── collectCallers() 递归收集
│   │   ├── 对每个 item:
│   │   │   ├── client.IncomingCalls()
│   │   │   └── 对每个 caller:
│   │   │       ├── 去重检查 (seen map)
│   │   │       ├── 追加 CallResult{Name, FilePath, Line, Column, Depth}
│   │   │       └── 如果 depth < maxDepth → 递归
│   │   └── data.Results = results, data.Total = len(results)
│   └── return CallHierarchyData
│
└── FormatCallHierarchyData(data)
    ├── 无结果: "No callers found" / "No callees found"
    └── 有结果: formatCallResultsWithDepth()
        └── === Callers (depth 1-3, 15 total) ===
            --- Depth 1 (3 functions) ---
             main at main.go:L42:C1
```

`GetCallees`/`GetCalleesData` 结构完全对称，使用 `OutgoingCalls`。

**关键设计**：
- `itemKey()` 生成 `URI:Line:Character` 格式的唯一键用于去重
- depth 限制在 1-10 之间
- 结果按深度分组输出
- `trimFileURI()` 使用 `protocol.ParseDocumentUri()` 正确解析 URI 路径

#### diagnostics.go — 诊断信息 (GPT5.5 重写)

GPT5.5 将诊断工具重构为数据/格式分离模式：

```go
type DiagnosticItem struct {
    Severity string `json:"severity"`
    Line     int    `json:"line"`
    Column   int    `json:"column"`
    Message  string `json:"message"`
    Source   string `json:"source,omitempty"`
    Code     any    `json:"code,omitempty"`
    FilePath string `json:"filePath"`
}

type DiagnosticsData struct {
    FilePath     string           `json:"filePath"`
    Total        int              `json:"total"`
    ErrorCount   int              `json:"errorCount"`
    WarningCount int              `json:"warningCount"`
    InfoCount    int              `json:"infoCount"`
    HintCount    int              `json:"hintCount"`
    Items        []DiagnosticItem `json:"items"`
    ContextLines []SourceLine     `json:"contextLines,omitempty"`
}

type SourceLine struct {
    Line    int    `json:"line"`
    Content string `json:"content"`
}
```

调用流程：

```
GetDiagnosticsForFile(ctx, client, filePath, contextLines, showLineNumbers)
│
├── GetDiagnosticsDataForFile(ctx, client, filePath, contextLines, includeContext)
│   ├── client.OpenFile()
│   ├── ⚠️ time.Sleep(3s) — 等待诊断推送（仍然存在，TODO: 事件驱动）
│   ├── client.Diagnostic() — 主动请求诊断
│   ├── client.GetFileDiagnostics() — 从缓存读取
│   ├── 按严重程度分类计数 (Error/Warning/Info/Hint)
│   ├── 构建 DiagnosticItem 列表
│   ├── 读取文件内容
│   ├── GetLineRangesToDisplay() 计算需显示的行范围
│   └── 构建 SourceLine 列表
│
└── FormatDiagnosticsData(data, showLineNumbers)
    ├── 无诊断: "No diagnostics found for <filePath>"
    └── 有诊断:
        /path/to/file.go
        Diagnostics in File: 5
        ERROR at L42:C15: use of undeclared identifier 'buffer' (Source: gopls, Code: undeclared)

           42 | func process() {
           43 | buffer := getData()
```

**⚠️ 关键遗留问题**：3 秒 `time.Sleep` 仍然存在（`diagnostics.go:103`），这是当前代码中最大的性能问题。GPT5.5 未修复此问题，仅添加了 TODO 注释。

#### lsp-utilities.go — LSP 共享工具 (GPT5.5 新增/重构)

提取了两个之前散布在多个文件中的共享函数：

##### GetFullDefinition — 获取完整定义代码块

```
GetFullDefinition(ctx, client, startLocation) → (string, protocol.Location, error)
│
├── client.DocumentSymbol() — 获取文档中所有符号
├── searchSymbols() 递归搜索包含 startLocation 的符号
│   └── 支持嵌套子符号 (DocumentSymbol.Children)
│
├── 找到符号范围:
│   ├── 读取文件内容
│   ├── 扩展 start 到行首 (Character = 0)
│   │
│   ├── 检测结束行最后一个字符:
│   │   ├── 如果是开括号 (, [, {, < →
│   │   │   └── 括号匹配扫描: 逐行向下寻找匹配闭括号
│   │   │       ├── 维护 bracketStack
│   │   │       └── 找到匹配 → 更新 symbolRange.End
│   │   └── 否则 → 使用 LSP 返回的范围
│   │
│   └── 返回范围行内容 + 更新后的 Location
│
└── 未找到 → 返回错误
```

**括号匹配**特别处理了 Python 等语言中定义范围不完整的问题：LSP 可能只返回函数签名的范围（以 `(` 结尾），工具会自动向下扫描找到匹配的 `)`。

##### GetLineRangesToDisplay — 计算需要显示的行范围

```
GetLineRangesToDisplay(ctx, client, locations, totalLines, contextLines) → map[int]bool
│
├── 对每个 location:
│   ├── GetFullDefinition() 查找容器定义
│   │
│   ├── 容器找到:
│   │   ├── 添加容器起始行
│   │   ├── 添加引用行
│   │   └── 添加上下文行（限制在容器范围内）
│   │
│   └── 容器未找到:
│       ├── 添加引用行
│       └── 添加上下文行（无容器范围限制）
│
└── 返回所有需要显示的行号集合
```

#### symbol_resolver.go — 符号名→位置解析 (GPT5.5 新增)

为 `callers`/`callees` 的 `symbolName` 参数提供支持：

```
ResolveSymbolLocation(ctx, client, symbolName, filePath) → (protocol.Location, error)
│
├── client.Symbol(WorkspaceSymbolParams{Query: symbolName})
│   └── workspace/symbol 请求
│
├── 遍历结果:
│   ├── symbolNameMatches(candidate, query)?
│   │   ├── 精确匹配: candidate == query → true
│   │   ├── 限定名拆分: "Type.Method" 或 "NS::Func" → 取最后部分匹配
│   │   └── 后缀匹配: candidate 以 "::query" 或 ".query" 结尾 → true
│   │
│   ├── filePath 非空? → locationMatchesFilePath(loc, filePath)
│   │   └── URI 路径 == filePath 或后缀匹配
│   │
│   └── 首个匹配 → 返回 Location
│
└── 无匹配 → 错误: symbol "X" not found [in "Y"]
```

**设计原则**：保守匹配——精确匹配优先，filePath 作为可选消歧提示。

#### logging.go — 工具组件日志 (GPT5.5 新增)

```go
var toolsLogger = logging.NewLogger(logging.Tools)
```

将之前各工具文件中的 `fmt` 输出或内联日志替换为组件化日志，支持通过环境变量独立控制日志级别。

#### ripgrep.go — 文本搜索 (L1)

```
SearchCode(ctx, workspaceDir, pattern, opts)
│
├── 构建 rg 命令参数:
│   --json --max-count N
│   [--ignore-case] [--word-regexp]
│   [-C contextLines] [-t fileType] [--glob include]
│   <pattern> .
│
├── exec.Command("rg", args...) 在 workspaceDir 下执行
│
└── parseRipgrepOutput():
    ├── 逐行解析 JSON
    ├── type="match": 文件头 + 行号 + 内容
    ├── type="context"/"begin"/"end": 上下文行
    └── 输出格式:
        === path/to/file.go ===
        42: int getUserInput(char *buf)
```

**注意**：依赖系统安装的 `rg` 命令，不是内嵌的。

#### treesitter.go — AST 查询 (L2)

```
RunTreesitterQuery(ctx, workspaceDir, query, filePath, language)
│
├── 有 filePath?
│   ├── NewParser(language)
│   ├── ParseFile(filePath) → tree, source
│   └── RunQuery(tree, source, lang, query) → results
│
└── 无 filePath?
    └── QueryDirectory(workspaceDir, query, language)
        ├── 遍历所有 C/C++ 文件
        ├── 每个文件 Parse → Query
        └── 合并结果

GetAST(ctx, filePath, nodeType, maxDepth)
│
├── DetectLanguage(filePath)
├── NewParser(lang)
├── ParseFile(filePath) → tree, source
├── TreeToAST(tree, source, maxDepth) → ASTNode
│
└── 有 nodeType?
    └── FilterByType(ast, nodeType) → 匹配节点列表
```

#### find_struct_usage.go — 结构体分析 (L2)

使用 tree-sitter CSP 查询：

```
FindStructUsage:     ((type_identifier) @type (#eq? @type "StructName"))
FindStructDefinition: (struct_specifier name: (type_identifier) @name (#eq? @name "StructName")) @struct
```

#### hover.go — 悬停信息

```
GetHoverInfo(ctx, client, filePath, line, column)
│
├── client.OpenFile()
├── 1-indexed → 0-indexed 转换
├── client.Hover(HoverParams{TextDocument, Position})
│
└── 有内容? → 返回 MarkupContent
    无内容? → 提取当前行文本 + 提示无悬停信息
```

#### edit_file.go — 文本编辑

```
ApplyTextEdits(ctx, client, filePath, edits[])
│
├── client.OpenFile()
├── 按 StartLine 降序排序（从底部处理，避免行号偏移）
│
├── 对每个 edit:
│   ├── getRange() 计算 protocol.Range
│   │   ├── 读取文件内容
│   │   ├── 检测行结束符风格 (CRLF/LF)
│   │   ├── 1-indexed → 0-indexed
│   │   └── 处理 EOF 定位
│   └── 构造 protocol.TextEdit
│
├── utilities.ApplyWorkspaceEdit() 应用编辑
└── 返回 "Successfully applied text edits. N lines removed, N lines added."
```

---

### 3.6 Tree-sitter 核心 (internal/tools/treesitter/)

#### parser.go — 解析器封装

```go
type Parser struct {
    p    *sitter.Parser   // tree-sitter 解析器
    lang *sitter.Language // 语言定义
}
```

- 支持 C 和 C++ 两种语言
- `NewParser(language)` 根据语言字符串或文件扩展名选择解析器
- `ParseFile()` 读取文件并解析为 AST
- `DetectLanguage()` 根据扩展名自动检测语言

#### query.go — CSP 查询

CSP (Captured S-expression Pattern) 是 tree-sitter 的查询语言：

```go
type QueryResult struct {
    Node     *sitter.Node // 匹配节点
    Capture  string       // 捕获名 (如 @func, @type)
    Content  string       // 节点文本内容
    FilePath string       // 所属文件
    Line     uint32       // 行号 (1-indexed)
    Column   uint32       // 列号 (1-indexed)
}
```

**RunQuery** 流程：
1. `sitter.NewQuery(pattern, lang)` — 编译查询
2. `sitter.NewQueryCursor()` — 创建游标
3. `qc.Exec(q, tree.RootNode())` — 执行查询
4. 遍历 `qc.NextMatch()` — 逐个匹配
5. `qc.FilterPredicates(match, source)` — 过滤谓词（如 `#eq?`）
6. 提取 Capture 名称、节点内容、位置信息

**QueryDirectory**：遍历目录中的 `.c/.h/.cpp/.cxx/.cc/.hpp/.hxx` 文件，逐一解析执行。

#### ast.go — AST 工具

```go
type ASTNode struct {
    Type     string       // 节点类型 (如 "function_definition")
    Content  string       // 节点文本
    StartRow uint32       // 起始行 (0-indexed)
    StartCol uint32       // 起始列
    EndRow   uint32       // 结束行
    EndCol   uint32       // 结束列
    Depth    int          // 树深度
    Children []*ASTNode   // 子节点
}
```

- `TreeToAST()` — 递归转换 tree-sitter 树为 ASTNode（受 maxDepth 限制）
- `FilterByType()` — 按节点类型递归过滤
- `FindAllDescendants()` — 按多个类型查找所有后代
- `String()` — 缩进格式化输出

---

### 3.7 文件监视器 (internal/watcher/)

#### 核心架构

```
WorkspaceWatcher
│
├── client LSPClient               # LSP 客户端接口
├── workspacePath string           # 工作区路径
├── config *WatcherConfig          # 配置
├── debounceMap map[string]*Timer  # 防抖定时器
├── registrations []FileSystemWatcher # LSP 服务器注册的监视模式
└── gitignore *GitignoreMatcher    # Gitignore 匹配器
```

#### WatchWorkspace 流程

```
WatchWorkspace(ctx, workspacePath)
│
├── NewGitignoreMatcher(workspacePath) # 解析 .gitignore
├── RegisterFileWatchHandler() # 注册 LSP 服务器监视注册回调
│
├── fsnotify.NewWatcher() # 创建 fsnotify 监视器
├── filepath.WalkDir() # 递归添加目录到监视
│   └── 跳过排除的目录
│
└── 事件循环:
    ├── ctx.Done() → 退出
    │
    ├── Create 事件:
    │   ├── 目录: watcher.Add() + 非排除目录
    │   └── 文件: openMatchingFile()
    │
    ├── Write 事件:
    │   └── 匹配注册模式? → debounceHandleFileEvent(Changed)
    │
    ├── Remove 事件:
    │   └── 匹配注册模式? → handleFileEvent(Deleted)
    │
    └── Rename 事件:
        ├── handleFileEvent(Deleted)
        └── 文件存在? → debounceHandleFileEvent(Created)
```

#### 文件事件处理智能路由

```
handleFileEvent(ctx, uri, changeType)
│
├── 文件已打开 AND 变更事件?
│   └── client.NotifyChange() # didChange (保持 LSP 文档同步)
│
└── 其他情况?
    └── client.DidChangeWatchedFiles() # 通知 LSP 服务器
```

#### 防抖机制

```go
debounceHandleFileEvent(ctx, uri, changeType)
// key = "uri:changeType"
// 已有定时器? → Stop()
// 新建 AfterFunc(300ms, handleFileEvent)
// 执行后删除定时器
```

300ms 防抖避免快速连续保存事件轰炸 LSP 服务器。

#### 排除规则

| 规则 | 说明 |
|------|------|
| 点目录/文件 | `.git`, `.vscode` 等 |
| 排除目录 | `node_modules`, `dist`, `build`, `out`, `bin`, `target`, `vendor` 等 |
| 排除扩展名 | `.swp`, `.swo`, `.tmp`, `.o`, `.so`, `.dll`, `.exe`, `.lock` |
| 二进制扩展名 | `.png`, `.jpg`, `.zip`, `.tar`, `.pdf`, `.mp3`, `.mp4`, `.wasm` |
| 大小限制 | > 5MB 的文件 |
| Gitignore | 匹配 `.gitignore` 中的模式 |

#### 文件批量打开

当 LSP 服务器注册文件监视器时，`AddRegistrations()` 启动后台 goroutine：

```
go func() {
    filepath.WalkDir(workspacePath, func(path, d, err):
    ├── 跳过排除目录
    ├── 跳过排除文件
    ├── isPathWatched(path)? → client.OpenFile(path)
    └── 每 100 个文件 sleep(10ms) # 避免过载
)
}()
```

#### Glob 模式匹配

支持 LSP 服务器注册的各种 Glob 模式：

- `**/*` — 匹配所有文件
- `**/*.go` — 匹配所有 Go 文件（后缀匹配）
- `*.{go,mod,sum}` — 大括号扩展（拆分为多个模式逐个匹配）
- `RelativePattern` — 带 BaseURI 的相对模式

---

### 3.8 缓存系统 (internal/tools/cache/)

GPT5.5 新增的缓存模块，为 Router 和工具提供线程安全的搜索结果缓存。

#### Cache 结构

```go
type Cache struct {
    items map[string]*CacheItem // 缓存项
    mu    sync.RWMutex          // 读写锁
    ttl   time.Duration         // 默认 TTL
}

type CacheItem struct {
    Value     interface{}  // 缓存值
    Timestamp time.Time    // 创建时间
    TTL       time.Duration // 过期时间
}
```

**特性**：
- 线程安全：`sync.RWMutex` 保护
- TTL 过期：每个 item 可独立设置 TTL，0 表示永不过期
- 主动清理：`Cleanup()` 删除所有过期项
- 默认 TTL：5 分钟

#### SearchResultCache

```go
type SearchResultCache struct {
    *Cache // 组合通用 Cache
}
```

#### 缓存键生成

```go
SearchCacheKey(query, strategy, filePath, language) → "query:strategy:filePath:language:"
StructCacheKey(structName, filePath, language) → "struct:structName:filePath:language:"
CallHierarchyCacheKey(filePath, line, column, depth) → "call:filePath:line:column:depth:"
```

**注意**：缓存键使用简单的字符串拼接（`generateKey`），不含哈希。对超长查询可能有键冲突风险，但在实际使用中几乎不会发生。

#### Router 中的缓存集成

```
Router.Search():
├── 生成缓存键 → cache.Get(key)
│   ├── 命中 → 直接返回
│   └── 未命中 → 执行搜索 → cache.Set(key, results)
│
├── NewRouter() → 默认 5 分钟 TTL
├── NewRouterWithCache(workspaceDir, cacheTTLSeconds) → 自定义 TTL
├── ClearCache() → 清空所有缓存
└── CacheSize() → 返回缓存项数量
```

---

### 3.9 日志系统 (internal/logging/)

#### 组件化设计

6 个独立组件，每个组件可设置不同日志级别：

| 组件 | 常量 | 用途 |
|------|------|------|
| `core` | `Core` | 主流程（启动、关闭、工具注册） |
| `lsp` | `LSP` | LSP 高层操作（调用方法、响应） |
| `wire` | `LSPWire` | LSP 线路协议（原始 JSON 报文） |
| `lsp-process` | `LSPProcess` | LSP 服务器进程输出（stderr） |
| `watcher` | `Watcher` | 文件监视事件 |
| `tools` | `Tools` | 工具执行日志 |

#### 日志级别

```
DEBUG → INFO → WARN → ERROR → FATAL
```

#### 环境变量配置

```bash
# 全局级别
LOG_LEVEL=DEBUG

# 组件级别（覆盖全局）
LOG_COMPONENT_LEVELS=wire:DEBUG,tools:INFO,lsp:WARN

# 日志文件（同时输出到 stderr 和文件）
LOG_FILE=/path/to/log
```

#### 使用方式

```go
// 各组件独立创建日志实例
var coreLogger = logging.NewLogger(logging.Core)  // main.go
var toolsLogger = logging.NewLogger(logging.Tools) // tools/logging.go

coreLogger.Debug("Sending message: method=%s id=%v", method, id)
coreLogger.Info("Server starting")
coreLogger.Warn("Shutdown request timed out")
coreLogger.Error("Failed to close: %v", err)
coreLogger.Fatal("Config error: %v", err) // 退出进程
```

---

### 3.10 工具函数 (internal/utilities/)

#### edit.go — WorkspaceEdit 应用

支持 LSP 协议中 `WorkspaceEdit` 的所有操作类型：

| 操作 | 方法 | 说明 |
|------|------|------|
| 文本编辑 | `ApplyTextEdits()` | 多重编辑，从底部到顶部应用 |
| 文件创建 | `ApplyDocumentChange()` | 支持覆盖/忽略已存在选项 |
| 文件删除 | `ApplyDocumentChange()` | 支持递归删除选项 |
| 文件重命名 | `ApplyDocumentChange()` | 支持覆盖保护 |
| TextDocumentEdit | `ApplyDocumentChange()` | 带版本号的文本编辑 |

**文本编辑应用流程**：

```
ApplyTextEdits(uri, edits[])
│
├── 读取文件内容
├── 检测行结束符 (CRLF/LF)
├── 检查重叠编辑
├── 按 (行号, 列号) 降序排序
│
├── 对每个 edit:
│   ├── ApplyTextEdit(lines, edit, lineEnding)
│   │   ├── 提取 startLine 的前缀
│   │   ├── 提取 endLine 的后缀
│   │   ├── 拼接 prefix + newText + suffix
│   │   └── 替换 lines 数组中的对应行
│   └── 更新 lines 数组
│
└── osWriteFile() 写回文件
```

**关键设计**：降序处理避免行号偏移问题。

---

### 3.11 LSP 协议代码生成 (cmd/generate/)

`internal/protocol/` 下的类型和 `internal/lsp/methods.go` 不是手写的，而是从 vscode-languageserver-node 的 TypeScript 定义自动生成的。

生成流程：

```
cmd/generate/
├── main.go     # 入口
├── types.go    # 类型定义（解析 TS 源码）
├── tables.go   # 查找表（Kind 映射等）
├── typenames.go # 类型名提取
├── methods.go  # 生成 LSP 方法
└── output.go   # 代码格式化输出

执行: go run ./cmd/generate
```

这保证了与 LSP 3.16+ / 3.17+ / 3.18 规范的兼容性，新增 LSP 方法只需重新生成即可。

---

### 3.12 符号解析器 (symbol_resolver.go)

GPT5.5 新增的模块，为 `callers`/`callees` 提供 "符号名 → LSP 位置" 的解析能力。

**核心函数**：

```go
func ResolveSymbolLocation(ctx context.Context, client *lsp.Client, symbolName, filePath string) (protocol.Location, error)
```

**设计原则**：

1. **保守匹配** — 精确匹配优先，避免误匹配
2. **filePath 作为消歧提示** — 当工作区中存在多个同名符号时，filePath 可帮助缩小范围
3. **限定名拆分** — 支持 `Type.Method` 和 `NS::Func` 格式，提取最后部分匹配
4. **后缀匹配** — 允许 `candidate` 以 `::query` 或 `.query` 结尾的匹配

**使用场景**：

- LLM 在对话模式下可能只知函数名不知行号：`callers(symbolName="getUserInput")`
- 与精确位置模式互补：`callers(filePath="main.cpp", line=42, column=5)`

---

### 3.13 MCP App UI 资源 (ui_resources.go)

GPT5.5 新增的模块，提供两个 MCP App UI 资源，使 MCP 客户端可以渲染交互式 UI：

#### 注册的 UI 资源

| 资源 URI | 名称 | 用途 | MIME 类型 |
|----------|------|------|-----------|
| `ui://call-hierarchy/graph` | Call Hierarchy Graph | 调用层级可视化图 | `text/html;profile=mcp-app` |
| `ui://diagnostics/dashboard` | Diagnostics Dashboard | 诊断信息仪表盘 | `text/html;profile=mcp-app` |

#### MCP App 工作原理

1. `tools.go` 中为 `callers`/`callees`/`diagnostics` 工具设置 `Meta` 字段，指向对应的 UI 资源 URI
2. 工具调用返回时，使用 `newAppToolResult(data, text, resourceURI)` 同时返回：
   - **结构化数据** (`structuredContent`) — Go struct 的 JSON 序列化
   - **文本回退** — `FormatXxxData()` 的输出（给不支持 UI 的客户端）
   - **UI 资源元数据** — 指向 HTML 资源的 URI
3. MCP 客户端读取 UI 资源，获取 HTML 模板
4. HTML 中的 `mcpAppBootstrapScript` 监听 `message` 事件，接收结构化数据并渲染

#### Bootstrap 脚本机制

```javascript
// 核心逻辑:
window.addEventListener("message", (event) => {
    const payload = getStructuredPayload(event); // 从 MCP 响应提取 structuredContent
    if (!payload) return;
    state.data = payload;
    renderFunction(payload); // 调用各自的渲染函数
});
```

#### Call Hierarchy Graph UI

- 左侧面板：显示方向(incoming/outgoing)、总数、最大深度
- 右侧面板：按深度排列的调用节点卡片
- 每个卡片显示：函数名 + 文件路径:行号:列号
- 响应式布局，窄屏自动切换为单列

#### Diagnostics Dashboard UI

- 顶部面板：文件名 + 严重程度统计（Total/Errors/Warnings/Info/Hints）
- 左侧面板：诊断项列表，每项显示严重程度 + 消息 + 位置
- 右侧面板：带行号的源代码上下文
- 严重程度用颜色区分：ERROR(红) / WARNING(橙) / INFO(蓝) / HINT(绿)
- 响应式布局

---

## 4. 数据流分析

### 4.1 典型场景：LLM 通过符号名查找调用链

```
Claude Desktop
│
│ MCP "callers" tool call
│ {symbolName: "getUserInput", depth: 3}
▼
tools.go handler
│ 提取参数, 无 filePath/line/column 但有 symbolName
▼
symbol_resolver.go: ResolveSymbolLocation("getUserInput", "")
│
├── client.Symbol(WorkspaceSymbolParams{Query: "getUserInput"})
│   └── ←─ 返回 SymbolInformation[]
│
├── symbolNameMatches(): 精确匹配 "getUserInput"
│
└── 返回 Location{URI: "file:///src/main.cpp", Range: {Line:42, Character:5}}
│
▼
call_hierarchy.go: GetCallersData("main.cpp", 43, 6, 3)
│
├── client.OpenFile("/src/main.cpp")
│   └── Notify("textDocument/didOpen", ...) ─────→ LSP 服务器
│
├── client.PrepareCallHierarchy(params)
│   └── Call("textDocument/prepareCallHierarchy", ...) ──→ LSP 服务器
│   └── ←─ 返回 CallHierarchyItem[]
│
└── collectCallers() 递归:
    │
    ├── [Depth 1]
    │   ├── Call("callHierarchy/incomingCalls", item1) ──→ LSP
    │   │   └── ←─ IncomingCall[]
    │   ├── 去重 + 记录 CallResult{Depth:1}
    │   └── ...
    │
    ├── [Depth 2]
    │   └── 对 Depth 1 的每个 caller 递归 incomingCalls
    │
    └── [Depth 3]
        └── 对 Depth 2 的每个 caller 递归 incomingCalls

FormatCallHierarchyData(data)
│ 按深度分组格式化
▼
newAppToolResult(CallHierarchyData, text, "ui://call-hierarchy/graph")
│ 同时返回结构化数据 + 文本回退 + UI 资源 URI
▼
MCP 客户端
│
├── 支持 UI → 渲染调用层级图
└── 不支持 UI → 显示文本回退
│
▼
LLM 理解调用链 → 安全分析
```

### 4.2 典型场景：统一搜索 (auto 模式)

```
Claude Desktop
│ MCP "search" tool call
│ {query: "strcpy", strategy: "auto", intent: "function"}
▼
tools.go handler
│
▼
router.Search(opts)
│
├── 缓存未命中
│
├── routeByIntent("function") → "ast"
│
└── searchAST(ctx, opts)
    │
    ▼
tools.RunTreesitterQuery(ctx, workspaceDir, "strcpy", "", "cpp")
    │
    ├── QueryDirectory()
    │   ├── 遍历所有 .c/.cpp 文件
    │   ├── ParseFile() → tree
    │   └── RunQuery(tree, source, lang, "strcpy")
    │       └── CSP 匹配: ((identifier) @id (#eq? @id "strcpy"))
    │
    └── 格式化结果

缓存结果 → 返回 MCP Tool Result
```

### 4.3 典型场景：诊断查询 (数据/格式分离 + UI)

```
Claude Desktop
│ MCP "diagnostics" tool call
│ {filePath: "/src/main.cpp", contextLines: 5}
▼
tools.go handler
│
▼
diagnostics.go: GetDiagnosticsDataForFile(ctx, client, "/src/main.cpp", 5, true)
│
├── client.OpenFile("/src/main.cpp")
├── time.Sleep(3s) ⚠️ — 等待诊断推送
├── client.Diagnostic() — 主动请求
├── client.GetFileDiagnostics() — 从缓存读取
│
├── 分类计数 (Error/Warning/Info/Hint)
├── 构建 []DiagnosticItem
├── GetLineRangesToDisplay() → 计算需显示的行范围
├── 构建 []SourceLine
│
└── 返回 DiagnosticsData{...}
    │
    ▼
tools.go handler:
│ data = DiagnosticsData
│ text = FormatDiagnosticsData(data, true)
│
├── newAppToolResult(data, text, "ui://diagnostics/dashboard")
│   ├── structuredContent: DiagnosticsData (JSON)
│   ├── 文本回退: 格式化诊断输出
│   └── Meta: ui://diagnostics/dashboard
│
▼
MCP 客户端
│
├── 支持 UI → 渲染诊断仪表盘
└── 不支持 UI → 显示文本:
    /src/main.cpp
    Diagnostics in File: 2
    ERROR at L42:C15: use of undeclared identifier 'buffer'
    ...
```

### 4.4 典型场景：文件变更通知

```
开发者保存 src/main.cpp
│
▼
fsnotify 检测到 Write 事件
│
▼
Watcher 事件循环
│
├── shouldExcludeFile? → 否
├── isPathWatched? → 是 (WatchChange)
│
├── debounceHandleFileEvent(ctx, uri, Changed)
│   └── 300ms 定时器启动
│
│ ... 300ms 内无新事件 ...
│
├── handleFileEvent(ctx, uri, Changed)
│   │
│   ├── 文件已打开?
│   │   └── client.NotifyChange(filePath)
│   │       └── Notify("textDocument/didChange", {
│   │           Version: 3,
│   │           ContentChanges: [{Text: "新内容"}]
│   │       }) ──→ LSP 服务器
│   │
│   └── 文件未打开?
│       └── client.DidChangeWatchedFiles({
│           Changes: [{URI, Type: Changed}]
│       }) ──→ LSP 服务器
│
└── LSP 服务器重新分析文件 → 推送新诊断
```

---

## 5. 设计亮点与已知局限

### 设计亮点

| # | 设计 | 说明 |
|---|------|------|
| 1 | **进程隔离** | LSP 服务器作为子进程，崩溃不影响主服务；通过管道通信，协议清晰 |
| 2 | **三层搜索** | 不同场景选不同层：L1 快但无语义，L3 慢但语义完整 |
| 3 | **智能路由** | intent-based routing，LLM 只需描述意图，Router 自动选择最佳搜索层 |
| 4 | **并行搜索** | `searchAll()` 三个 goroutine 并行，取所有层结果 |
| 5 | **父进程死亡检测** | 每 100ms 轮询 ppid，解决 Claude Desktop 不正确杀子进程的实际 bug |
| 6 | **代码生成** | LSP 协议类型自动从 TypeScript 定义生成，减少手工维护 |
| 7 | **防抖通知** | 300ms debounce 减少 LSP 服务器负载 |
| 8 | **Gitignore 支持** | 自动排除不需要监视的文件 |
| 9 | **调用链去重** | seen map 避免调用链循环和重复 |
| 10 | **组件化日志** | 6 个组件独立级别控制，wire 组件可查看原始报文 |
| 11 | **编辑降序处理** | 文本编辑从底部到顶部处理，避免行号偏移 |
| 12 | **数据/格式分离** (GPT5.5) | GetXxxData + FormatXxxData 模式，支持结构化输出和 MCP App UI |
| 13 | **MCP App UI** (GPT5.5) | 调用层级图和诊断仪表盘，交互式可视化 |
| 14 | **符号名解析** (GPT5.5) | ResolveSymbolLocation 允许通过符号名而非位置调用工具 |
| 15 | **搜索缓存** (GPT5.5) | TTL 缓存集成到 Router，减少重复搜索开销 |
| 16 | **括号匹配扩展** (GPT5.5) | GetFullDefinition 自动补全括号不完整的定义范围 |
| 17 | **辅助函数提取** (GPT5.5) | readIntArgument/appToolMeta/newAppToolResult 减少工具注册样板代码 |

### 已知局限与改进方向

| # | 局限 | 当前实现 | 改进方向 |
|---|------|----------|----------|
| 1 | 服务器就绪检测 | `WaitForServerReady()` 只是 `sleep(1s)` | 等待特定初始化消息或轮询 `workspace/symbol` |
| 2 | 诊断等待 ⚠️ | `GetDiagnosticsDataForFile()` 仍用 `sleep(3s)` | 改为事件驱动，等待 `publishDiagnostics` 通知 |
| 3 | L3 symbol 后备 ⚠️ | `searchSymbol()` 实际用 ripgrep，非 LSP `workspace/symbol` | 接入真正的 LSP 符号搜索（需 Router 持有 LSP Client 引用） |
| 4 | Tree-sitter 语言支持 | 仅 C/C++ | 扩展 Go、Rust、Python 等 |
| 5 | 文件同步模式 | 全量内容模式（`TextDocumentSyncKindFull`） | 对大文件改用增量模式 |
| 6 | 缓存失效 | 基于 TTL，文件变更不会主动失效缓存 | Watcher 检测到变更时清空相关缓存 |
| 7 | 错误处理 | 部分工具静默忽略 LSP 错误 | 增加重试机制和更详细的错误报告 |
| 8 | 缓存键冲突 | 简单字符串拼接，超长查询可能冲突 | 使用哈希（如 SHA256 摘要）生成缓存键 |
| 9 | Router 无 LSP 引用 | Router 完全不持有 LSP Client，无法调用语义搜索 | 将 LSP Client 注入 Router，searchSymbol() 可真正调 LSP |
| 10 | searchAll 重复工作 | L1 和 L3 层都是 ripgrep（仅大小写敏感不同） | 合并或去重两层的搜索逻辑 |

---

## 6. 开发环境使用

### 6.1 前置依赖

| 依赖 | 版本 | 安装方式 |
|------|------|----------|
| Go | >= 1.24.0 | <https://golang.org/doc/install> |
| ripgrep | 任意 | `apt install ripgrep` / `brew install ripgrep` |
| just (可选) | 任意 | `cargo install just` / `brew install just` |
| LSP 服务器 (按需) | — | 见下方 |

按项目语言安装对应的 LSP 服务器：

```bash
# Go
go install golang.org/x/tools/gopls@latest

# Rust
rustup component add rust-analyzer

# Python
npm install -g pyright

# TypeScript
npm install -g typescript typescript-language-server

# C/C++
brew install clangd # macOS
apt install clangd # Linux
```

### 6.2 克隆与构建

```bash
git clone https://github.com/isaacphi/mcp-language-server.git
cd mcp-language-server

# 构建
go build -o mcp-language-server
# 或使用 just
just build

# 安装到 $GOBIN
go install
# 或
just install
```

### 6.3 开发常用命令

项目包含 `justfile`，提供以下任务：

```bash
just -l        # 列出所有任务
just build     # 构建二进制
just install   # 安装到 $GOBIN
just fmt       # 格式化代码 (gofmt)
just generate  # 重新生成 LSP 协议类型和方法
just check     # 运行全部代码审计检查
just test      # 运行单元测试
just snapshot  # 更新集成测试快照
```

**`just check` 详细内容**：

```
1. gofmt -l .                    # 检查格式
2. go tool staticcheck ./...     # 静态分析
3. go tool errcheck ./...        # 错误处理检查
4. gopls check *.go              # Go 语义检查
5. go tool govulncheck ./...     # 漏洞检查
```

### 6.4 代码生成

如果修改了 LSP 协议类型或需要新增 LSP 方法：

```bash
go run ./cmd/generate
# 或
just generate
```

这会重新生成：
- `internal/protocol/tsprotocol.go` — LSP 协议类型
- `internal/lsp/methods.go` — LSP 方法封装

### 6.5 运行测试

#### 单元测试

```bash
go test ./...
# 或
just test
```

#### 集成测试（快照测试）

集成测试使用真实的 LSP 服务器在模拟工作区上运行工具：

```
integrationtests/
├── tests/       # 测试用例
├── workspaces/  # 模拟工作区 (Go/C/Rust/Python/TypeScript/Clangd)
├── snapshots/   # 工具输出快照
└── test-output/ # (gitignored) 测试运行后的工作区状态和日志
```

```bash
# 运行集成测试
go test ./integrationtests/...

# 更新快照（当工具输出格式变化时）
UPDATE_SNAPSHOTS=true go test ./integrationtests/...
# 或
just snapshot
```

**前提**：需要安装对应的 LSP 服务器才能运行对应语言的集成测试。

### 6.6 本地开发调试

#### 配置 Claude Desktop 使用本地构建

编辑 `~/Library/Application Support/Claude/claude_desktop_config.json`（macOS）或对应路径：

```json
{
  "mcpServers": {
    "language-server": {
      "command": "/full/path/to/your/clone/mcp-language-server/mcp-language-server",
      "args": [
        "--workspace",
        "/path/to/workspace",
        "--lsp",
        "gopls"
      ],
      "env": {
        "LOG_LEVEL": "DEBUG"
      }
    }
  }
}
```

修改代码后需要重新 `just build`，然后重启 Claude Desktop。

#### 日志调试

```bash
# 全局 DEBUG 级别
LOG_LEVEL=DEBUG mcp-language-server --workspace /path --lsp gopls

# 仅调试 LSP 线路协议
LOG_LEVEL=INFO LOG_COMPONENT_LEVELS=wire:DEBUG mcp-language-server --workspace /path --lsp gopls

# 日志写入文件
LOG_LEVEL=DEBUG LOG_FILE=/tmp/mcp-lsp.log mcp-language-server --workspace /path --lsp gopls

# 查看日志
tail -f /tmp/mcp-lsp.log
```

#### 直接命令行运行

```bash
# 基本运行
./mcp-language-server --workspace /path/to/project --lsp gopls

# 带 LSP 参数（-- 后的参数传递给 LSP 服务器）
./mcp-language-server --workspace /path/to/project --lsp pyright-langserver -- --stdio

# clangd 带 compile_commands.json 路径
./mcp-language-server --workspace /path/to/project --lsp clangd -- --compile-commands-dir=/path/to/build
```

MCP 服务器通过 stdio 通信，直接命令行运行时，输入需要是合法的 MCP JSON-RPC 消息。通常由 MCP 客户端（如 Claude Desktop）启动。

#### 环境变量参考

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `LOG_LEVEL` | `INFO` | 全局日志级别: DEBUG/INFO/WARN/ERROR/FATAL |
| `LOG_COMPONENT_LEVELS` | — | 组件级别覆盖，格式: `comp:LEVEL,comp:LEVEL` |
| `LOG_FILE` | — | 日志文件路径（同时输出到 stderr） |
| `LSP_CONTEXT_LINES` | `5` | 引用/诊断输出的上下文行数 |

---

## 7. 部署环境使用

### 7.1 一键安装（推荐）

```bash
go install github.com/isaacphi/mcp-language-server@latest
```

安装后 `mcp-language-server` 位于 `$GOBIN`（默认 `~/go/bin`）。

### 7.2 系统要求

| 要求 | 说明 |
|------|------|
| 操作系统 | Linux / macOS / Windows |
| Go 运行时 | 不需要（静态编译的二进制） |
| ripgrep | **必须**安装，L1 文本搜索依赖 `rg` 命令 |
| LSP 服务器 | **必须**安装对应语言的 LSP 服务器 |
| 内存 | 依赖 LSP 服务器，大项目可能需要 1-4 GB |

### 7.3 Claude Desktop 配置

#### Go 项目 (gopls)

```json
{
  "mcpServers": {
    "language-server": {
      "command": "mcp-language-server",
      "args": ["--workspace", "/Users/you/dev/yourproject/", "--lsp", "gopls"],
      "env": {
        "PATH": "/opt/homebrew/bin:/Users/you/go/bin",
        "GOPATH": "/Users/you/go",
        "GOCACHE": "/Users/you/Library/Caches/go-build",
        "GOMODCACHE": "/Users/you/go/pkg/mod"
      }
    }
  }
}
```

> **注意**：Claude Desktop 可能不会继承用户的 shell 环境变量，需要显式设置 `PATH`、`GOPATH`、`GOCACHE`、`GOMODCACHE`。通过 `echo $(which go):$(which gopls)` 获取路径。

#### Rust 项目 (rust-analyzer)

```json
{
  "mcpServers": {
    "language-server": {
      "command": "mcp-language-server",
      "args": [
        "--workspace", "/Users/you/dev/yourproject/",
        "--lsp", "rust-analyzer"
      ]
    }
  }
}
```

#### Python 项目 (pyright)

```json
{
  "mcpServers": {
    "language-server": {
      "command": "mcp-language-server",
      "args": [
        "--workspace", "/Users/you/dev/yourproject/",
        "--lsp", "pyright-langserver",
        "--", "--stdio"
      ]
    }
  }
}
```

> `--` 后的参数直接传递给 LSP 服务器。pyright 需要 `--stdio` 参数。

#### TypeScript 项目 (typescript-language-server)

```json
{
  "mcpServers": {
    "language-server": {
      "command": "mcp-language-server",
      "args": [
        "--workspace", "/Users/you/dev/yourproject/",
        "--lsp", "typescript-language-server",
        "--", "--stdio"
      ]
    }
  }
}
```

#### C/C++ 项目 (clangd)

```json
{
  "mcpServers": {
    "language-server": {
      "command": "mcp-language-server",
      "args": [
        "--workspace", "/Users/you/dev/yourproject/",
        "--lsp", "/path/to/clangd",
        "--", "--compile-commands-dir=/path/to/build"
      ]
    }
  }
}
```

> clangd 需要 `compile_commands.json`（由 CMake/Make 等生成）才能正常工作。

#### 其他 LSP 服务器

只要语言服务器通过 **stdio** 通信，就可以使用：

```json
{
  "mcpServers": {
    "language-server": {
      "command": "mcp-language-server",
      "args": [
        "--workspace", "/path/to/project/",
        "--lsp", "your-language-server",
        "--", "--any-server-args"
      ]
    }
  }
}
```

### 7.4 其他 MCP 客户端

除了 Claude Desktop，任何支持 MCP 的客户端都可以使用：

- **Cursor IDE** — 在 MCP 设置中添加相同的配置
- **VS Code (Cline/Continue 插件)** — 在插件 MCP 配置中添加
- **自定义客户端** — 通过 stdio 启动子进程，发送 MCP JSON-RPC 消息

### 7.5 生产部署注意事项

#### 资源消耗

| 组件 | 典型内存 | 说明 |
|------|----------|------|
| mcp-language-server | 20-50 MB | 本身很轻量 |
| LSP 服务器 | 500 MB - 4 GB | 取决于项目大小和语言 |
| tree-sitter 解析 | 每文件 1-10 MB | 仅 C/C++，按需解析 |
| 搜索缓存 | 通常 < 10 MB | 默认 5 分钟 TTL，仅存文本结果 |

**建议**：对于百万级 C/C++ 代码库，确保至少 4 GB 可用内存。

#### 冷启动时间

LSP 服务器需要时间索引项目：

| 语言服务器 | 典型冷启动 | 热启动 |
|------------|-----------|--------|
| gopls | 5-15s | <1s |
| clangd | 10-60s | 2-5s |
| rust-analyzer | 10-30s | 2-5s |
| pyright | 3-10s | <1s |

`WaitForServerReady()` 目前硬编码等待 1 秒，对于大型项目可能不够。可以设置 `LOG_LEVEL=DEBUG` 观察初始化日志确认服务器是否就绪。

**诊断额外延迟**：`diagnostics` 工具的 3 秒 `time.Sleep` 意味着每次调用至少耗时 3 秒以上，这是目前最大的延迟来源。

#### 多项目支持

每个 MCP 服务器实例只支持一个工作区和一个 LSP 服务器。多项目需要配置多个实例：

```json
{
  "mcpServers": {
    "go-server": {
      "command": "mcp-language-server",
      "args": ["--workspace", "/path/to/go-project/", "--lsp", "gopls"]
    },
    "cpp-server": {
      "command": "mcp-language-server",
      "args": ["--workspace", "/path/to/cpp-project/", "--lsp", "clangd", "--", "--compile-commands-dir=/path/to/build"]
    }
  }
}
```

#### 安全考虑

| 风险 | 缓解措施 |
|------|----------|
| LLM 通过 `edit_file` 修改源码 | LLM 有完整的代码编辑能力，建议配合版本控制（git）使用 |
| LLM 通过 `rename_symbol` 全局重命名 | 重命名前建议 LLM 先 `references` 确认影响范围 |
| LSP 服务器崩溃 | 进程隔离 + 自动 force kill（2 秒超时） |
| 子进程泄漏 | 父进程死亡检测 + 信号处理 |

#### 故障排查

```bash
# 1. 确认二进制存在
which mcp-language-server

# 2. 确认 ripgrep 可用
which rg

# 3. 确认 LSP 服务器可启动
gopls version
clangd --version

# 4. 开启 DEBUG 日志运行
LOG_LEVEL=DEBUG mcp-language-server --workspace /path --lsp gopls 2>/tmp/debug.log

# 5. 检查 LSP 通信报文
LOG_LEVEL=INFO LOG_COMPONENT_LEVELS=wire:DEBUG mcp-language-server --workspace /path --lsp gopls 2>/tmp/wire.log

# 6. 检查 LSP 服务器进程输出
LOG_LEVEL=INFO LOG_COMPONENT_LEVELS=lsp-process:DEBUG mcp-language-server --workspace /path --lsp gopls 2>/tmp/process.log
```

### 7.6 Docker 部署（可选）

虽然本项目不提供官方 Docker 镜像，但可以自行构建：

```dockerfile
FROM golang:1.24-alpine AS builder
RUN apk add --no-cache ripgrep clang
WORKDIR /build
COPY . .
RUN go build -o mcp-language-server

FROM alpine:latest
RUN apk add --no-cache ripgrep clang
COPY --from=builder /build/mcp-language-server /usr/local/bin/
ENTRYPOINT ["mcp-language-server"]
```

```bash
docker build -t mcp-language-server .
# 注意: MCP stdio 协议需要通过 docker run -i 交互模式使用
```

### 7.7 完整工具速查表

| 工具 | 层级 | 最佳场景 | 示例 |
|------|------|----------|------|
| `search` | L1/L2/L3 | 通用搜索，不确定用哪个工具时 | `search("strcpy", strategy="auto", intent="function")` |
| `search_text` | L1 | 查找 TODO、注释、字符串字面量 | `search_text("FIXME")` |
| `search_ast` | L2 | 查找特定 AST 结构 | `search_ast("(function_definition) @func")` |
| `search_symbol` | L3 | 查找符号定义/引用 ⚠️ 实际是 ripgrep | `search_symbol("MyStruct")` |
| `definition` | L3 | 读取符号完整实现 | `definition("MyFunction")` |
| `references` | L3 | 查找所有使用位置 | `references("MyType")` |
| `callers` | L3 | 追踪谁调用了这个函数 | `callers(symbolName="getUserInput", depth=3)` |
| `callees` | L3 | 追踪这个函数调用了谁 | `callees("main.cpp", 42, 5, depth=2)` |
| `diagnostics` | L3 | 检查文件错误/警告 | `diagnostics("main.cpp")` |
| `hover` | L3 | 查看类型信息和文档 | `hover("main.cpp", 42, 5)` |
| `rename_symbol` | L3 | 全局重命名 | `rename_symbol("main.cpp", 42, 5, "newName")` |
| `edit_file` | - | 修改文件内容 | `edit_file("main.cpp", [{startLine:10, endLine:12, newText:"..."}])` |
| `ripgrep` | L1 | 精确控制的正则搜索 | `ripgrep("pattern", caseSensitive=true, fileType="go")` |
| `treesitter_query` | L2 | CSP 模式匹配 AST | `treesitter_query("(call_expression) @call")` |
| `treesitter_ast` | L2 | 探索文件 AST 结构 | `treesitter_ast("main.cpp", nodeType="function_definition")` |
| `find_struct_usage` | L2 | 查找结构体所有使用 | `find_struct_usage("UserData")` |
| `find_struct_definition` | L2 | 查找结构体定义 | `find_struct_definition("UserData")` |

### 7.8 安全检视典型工作流

这是本项目的主要使用场景：

```
1. 理解项目结构
   search("main", strategy="auto", intent="function")

2. 定位目标函数
   definition("getUserInput")

3. 追踪上游调用链
   callers(symbolName="getUserInput", depth=3)

4. 追踪下游调用链
   callees(symbolName="getUserInput", depth=2)

5. 分析数据结构
   find_struct_usage("UserData")
   find_struct_definition("UserData")

6. 查找所有引用
   references("getUserInput")

7. 检查编译问题
   diagnostics("main.cpp")

8. 查看类型信息
   hover("main.cpp", 42, 5)

9. LLM 综合分析 → 安全报告
```

---

## 8. 三层搜索架构 vs 纯 LSP：价值分析

> 本节分析 ripgrep (L1) + tree-sitter (L2) 相比仅使用 clangd/gopls (L3) 的实际增益与不足。

### 8.1 ripgrep 带来的全新能力

以下能力是 LSP 服务器完全无法提供的：

| 能力 | 说明 | 安全检视场景 |
|------|------|-------------|
| **正则/任意文本搜索** | 支持任意 regex 模式，不限语言 | 搜索硬编码密钥、特定格式的字符串 |
| **非代码文件搜索** | `.md`/`.json`/`.yaml`/`Makefile`/`.cmake`/`.sh` 等全部可搜 | 检查配置文件、构建脚本、文档中的敏感信息 |
| **TODO/FIXME/注释搜索** | LSP 无法搜索注释和字符串内容 | 发现遗留的安全 TODO、FIXME 标记 |
| **大小写/全词匹配** | `--ignore-case`/`--word-regexp` 为一级选项 | 精确或模糊搜索符号名 |
| **文件类型过滤** | `-t go`/`--glob *.cpp` 跨语言过滤 | 按文件类型限定搜索范围 |
| **上下文行输出** | `-C N` 直接输出匹配周围的代码行 | 无需二次读取文件即可看到上下文 |
| **零启动延迟** | ripgrep 毫秒级响应，无需等待 LSP 索引 | 冷启动即可搜索，不等 clangd |
| **烂代码兼容** | 不依赖编译，无法编译的代码照常可搜 | 分析正在开发中、尚不能编译的代码 |
| **零配置** | 不需要 `compile_commands.json` | 没有构建系统的项目也能用 |

### 8.2 Tree-sitter 带来的全新能力

| 能力 | 说明 | 与 LSP 的区别 |
|------|------|--------------|
| **CSP/AST 模式查询** | 任意结构化模式匹配，如 `(function_definition) @func` | LSP 没有等价的 AST 查询语言 |
| **AST 可视化** | 完整 AST 树结构，含每个节点类型和位置 | LSP 的 `documentSymbol` 只返回语义大纲 |
| **结构体 type_identifier 全量匹配** | 找到所有语法上的类型名出现位置 | LSP 的 references 可能遗漏前向声明、typedef 别名、宏展开中的出现 |
| **烂代码兼容** | tree-sitter 有错误恢复能力，能部分解析有语法错误的文件 | LSP 无法索引编译不过的文件 |
| **无需 LSP 进程** | 编译进二进制，按需解析，无需子进程 | LSP 需要启动进程、索引项目、同步文件 |
| **无项目配置依赖** | 直接遍历文件系统解析 | LSP 需要 `compile_commands.json` 等 |

**当前限制**：tree-sitter 仅支持 C/C++，不支持 Go/Rust/Python/TypeScript。

### 8.3 LSP 的独有能力（L1/L2 无法替代）

| 能力 | 说明 | 为什么 ripgrep/tree-sitter 做不到 |
|------|------|-------------------------------|
| **语义定义解析** | 知道 `foo()` 调用对应哪个文件里的哪个定义，跨翻译单元、处理宏和模板 | ripgrep 只做文本匹配；tree-sitter 只做语法匹配，无法解析语义 |
| **语义引用查找** | `references` 精确返回指向同一符号的引用，不会混淆同名不同符号 | 同上：文本/语法匹配无法区分同名异义 |
| **调用链追踪** | `callers`/`callees` 通过语义调用层级递归追踪，支持 depth | 需要语义理解调用目标（包括函数指针、虚函数、重载） |
| **Hover/类型推导** | typedef 展开、模板实例化结果、推断类型 | tree-sitter 知道语法类型，不知道语义类型 |
| **编译诊断** | 类型错误、未初始化变量、缺失 include 等 | ripgrep/tree-sitter 不是编译器 |
| **安全重命名** | `rename_symbol` 只重命名语义上同一符号的所有出现 | ripgrep 无法重命名；tree-sitter 会把同名异义的也改了 |

### 8.4 层级重叠分析

三个层级在"查找符号"场景下存在有意重叠，形成**精度梯度**：

| 查找 "UserData" | L1 (ripgrep) | L2 (tree-sitter) | L3 (LSP) |
|-----------------|-------------|-----------------|----------|
| 匹配范围 | 任何文本出现（含注释、字符串） | 语法上的 `type_identifier` 节点 | 语义上指向该定义的引用 |
| 噪声 | 高（注释、字符串、同名变量） | 中（前向声明、typedef 别名） | 低（仅语义引用） |
| 精度 | 低 | 中 | 高 |
| 速度 | 最快 | 快 | 最慢 |

重叠是有意设计——更快的层可作为后备，Router 可按需选择精度级别。

### 8.5 关键架构问题：统一搜索的 L3 层是假的

**这是当前架构最严重的问题，GPT5.5 未修复。**

`searchSymbol()`（统一搜索的 L3 层）**根本没有调用 LSP**，它只是用 ripgrep 开了大小写敏感模式，然后把结果标记为 "symbol" 层：

```go
// router.go:165-179
func (r *Router) searchSymbol(ctx context.Context, opts SearchOptions) ([]SearchResult, error) {
    // 使用 ripgrep 作为符号搜索的后备方案
    // LSP 符号搜索需要精确的符号名称
    rgOpts := tools.RipgrepOptions{
        MaxCount:     100,
        CaseSensitive: true,
    }
    result, err := tools.SearchCode(ctx, r.workspaceDir, opts.Query, rgOpts)
    // ...
    return []SearchResult{
        {Layer: "symbol", Content: result, ...},
    }, nil
}
```

**影响**：

1. 当 LLM 使用 `search(query, strategy="auto")` 时，它以为三层都搜了，**实际上 L3 就是 L1 换了个标签**
2. `searchAll()` 并行跑三个 goroutine，但其中 "symbol" 和 "text" 层都是 ripgrep（仅大小写敏感不同），**做了重复工作**
3. 真正的 LSP 语义能力（definition、references、callers、callees、hover、diagnostics）**只能通过独立的 MCP 工具访问**，完全绕过了统一搜索路由器
4. LLM 如果只用 `search` 统一入口，**实际上只用了 L1 + L2，丢失了最有价值的语义搜索能力**

**根本原因**：Router 不持有 LSP Client 引用。Router 只依赖 `tools` 包（ripgrep 和 tree-sitter），完全不知道 LSP 的存在。要真正接入 L3，需要将 LSP Client 注入 Router。

**GPT5.5 提供了部分缓解**：新增的 `ResolveSymbolLocation()` 已经证明从符号名到 LSP 位置的解析是可行的。但这个函数只在 `callers`/`callees` 工具中使用，没有被 Router 采用。

### 8.6 路由关键词映射的潜在问题

`routeByIntent()` 的关键词匹配中，`"definition"` 被归入 L2 (ast) 而非 L3 (symbol)：

```
L2 (ast) 关键词: function, struct, class, node, ast, syntax, definition, declare
L3 (symbol) 关键词: symbol, reference, call, usage, import, type, variable
```

这意味着 `intent="definition"` 会路由到 tree-sitter 而非 LSP。但 `definition` MCP 工具（用户最可能关联"定义查找"的工具）使用的是 LSP。这可能让 LLM 产生困惑。

### 8.7 GPT5.5 的改进评估

| 维度 | 改进前 | GPT5.5 后 | 说明 |
|------|--------|-----------|------|
| 数据/格式分离 | 无，工具直接返回文本 | GetXxxData + FormatXxxData | 支持 MCP App UI 和结构化输出 |
| MCP App UI | 无 | 调用层级图 + 诊断仪表盘 | 可视化交互式 UI |
| 符号名调用 | callers/callees 只能通过行号列号 | 支持 symbolName 参数 | 更符合 LLM 对话模式 |
| 共享工具函数 | GetFullDefinition 等散布在个别文件 | 提取到 lsp-utilities.go | 定义/引用/诊断共享逻辑 |
| 搜索缓存 | 无 | TTL 缓存集成到 Router | 减少重复搜索 |
| 组件日志 | 部分工具用 fmt 输出 | 统一 toolsLogger | 可配置级别 |
| 诊断 3s sleep | 存在 | ⚠️ 仍然存在 | GPT5.5 添加了 TODO 但未修复 |
| L3 symbol 假冒 | 存在 | ⚠️ 仍然存在 | Router 仍无 LSP 引用 |

### 8.8 总结：到底有没有更好？

**有的，但收益不均匀：**

| 维度 | 相比纯 LSP 的改进程度 | 说明 |
|------|----------------------|------|
| 文本搜索能力 | **显著提升** | ripgrep 带来了 LSP 完全做不到的正则搜索、非代码文件搜索、TODO 搜索等 |
| AST 结构查询 | **显著提升** | tree-sitter 的 CSP 查询和 AST 可视化是 LSP 没有的全新能力 |
| 鲁棒性 | **明显提升** | 烂代码兼容、零配置可用、LSP 挂了还能用 L1/L2 |
| 冷启动速度 | **明显提升** | ripgrep 毫秒级，tree-sitter 秒级，LSP 可能要 60 秒 |
| 语义搜索 | **没有提升** | LSP 的语义能力仍然只能通过独立工具访问，未整合进统一搜索 |
| 统一搜索体验 | **反而有误导** | L3 层是假的，LLM 可能以为已经做了语义搜索但其实没有 |
| MCP App UI | **新增能力** (GPT5.5) | 调用层级图和诊断仪表盘提供了更好的可视化 |
| 符号名调用 | **新增能力** (GPT5.5) | callers/callees 支持符号名，更自然 |
| 搜索缓存 | **性能提升** (GPT5.5) | 重复搜索零延迟 |

**最终判断**：ripgrep 和 tree-sitter 确实带来了 LSP 做不到的重要能力，在安全检视场景下尤其有价值（搜索硬编码密钥、搜索 TODO/FIXME、分析烂代码等）。GPT5.5 的数据/格式分离、MCP App UI、符号名解析和搜索缓存都是有意义的改进。但两个最根本的问题仍未修复：统一搜索的 L3 层是假的，诊断查询仍有 3 秒阻塞等待。LLM 需要足够聪明，知道什么时候该用 `search`（L1/L2），什么时候该用 `definition`/`references`/`callers`（真正的 L3）。

### 8.9 改进建议

| 优先级 | 改进 | 说明 |
|--------|------|------|
| **P0** | `searchSymbol()` 接入真正的 LSP `workspace/symbol` | 将 LSP Client 注入 Router，LSP 就绪时使用语义搜索，不可用时回退到 ripgrep |
| **P0** | 消除诊断 3s sleep | 改为事件驱动：OpenFile 后等待 `publishDiagnostics` 通知（带超时） |
| **P1** | 消除 `searchAll()` 中 L1 和 L3 的重复工作 | 两者都是 ripgrep，差异仅大小写敏感，应合并或去重 |
| **P1** | 调整 `"definition"` 关键词到 L3 (symbol) | 与用户直觉和 `definition` 工具保持一致 |
| **P1** | 缓存主动失效 | Watcher 检测到文件变更时清空相关缓存 |
| **P2** | 扩展 tree-sitter 语言支持 | 至少增加 Go 和 Rust，与 LSP 服务器覆盖的语言对齐 |
| **P2** | 统一搜索结果中标注数据来源 | 让 LLM 知道 "symbol" 层结果是来自 LSP 还是 ripgrep 后备 |
| **P2** | 缓存键改用哈希 | 避免超长查询字符串导致的键冲突 |
| **P3** | 文件同步改用增量模式 | 对大文件减少传输量 |
