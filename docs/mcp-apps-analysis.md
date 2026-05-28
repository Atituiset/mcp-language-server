# MCP Apps 知识总览与 mcp-language-server 使用场景分析

> 生成日期: 2026-05-11
> 基于官方规范 SEP-1865 (2026-01-26 定稿)

---

## 第一部分：MCP Apps 核心知识

### 1.1 什么是 MCP Apps

**MCP Apps**（官方扩展标识: `io.modelcontextprotocol/ui`，别名 `io.modelcontextprotocol/apps`）是 MCP 协议的**第一个官方扩展**，于 2026 年 1 月 26 日正式定稿（SEP-1865）。

它让 MCP 服务器能够向宿主（Host）交付**富交互式用户界面**，突破了纯文本对话的局限。

**核心解决的问题**：
- 从 50+ 选项中选择时，纯文本对话效率低下
- 数据可视化（图表、地图）无法通过文本表达
- 多步骤工作流表单需要交互式控件
- 实时仪表盘需要动态更新

### 1.2 架构设计

```
+-------------+      +--------------+      +-------------+
|  MCP Server |<---->|  LLM Host    |<---->|  MCP App UI |
|             | JSON |  (Claude/    | App  | (sandboxed  |
| Tools/      | RPC  |   ChatGPT/   |Bridge|   iframe)   |
| Resources   |      |   VS Code)   |      |             |
| UI Resources|      |              |      |             |
+-------------+      +--------------+      +-------------+
```

**关键设计决策**：

| 设计点 | 说明 |
|--------|------|
| **UI 资源预声明** | 使用 `ui://` URI scheme 注册模板，宿主可预取和审查 |
| **双向通信复用协议** | UI ↔ Host 通过现有 MCP JSON-RPC 协议 + `postMessage`，不发明新格式 |
| **沙盒 iframe** | 内容类型为 `text/html;profile=mcp-app`，在沙盒 iframe 中渲染 |

### 1.3 安全模型

1. **Iframe 沙盒** — 限制权限运行
2. **模板预声明** — Host 渲染前可审查 HTML，禁止任意注入脚本
3. **消息可审计** — 所有通信都是结构化 JSON-RPC，可记录
4. **用户显式同意** — UI 发起的工具调用需要用户批准

### 1.4 资源注册示例

```json
// 服务器注册 UI 模板
{
  "uri": "ui://charts/bar-chart",
  "name": "Bar Chart Viewer",
  "mimeType": "text/html;profile=mcp-app"
}

// 工具关联 UI
{
  "name": "visualize_data_as_bar_chart",
  "_meta": {
    "ui/resourceUri": "ui://charts/bar-chart"
  }
}
```

### 1.5 生产模式（边缘部署）

常见部署在 **Cloudflare Workers** 等无状态边缘平台：
- **传输**：HTTP+SSE 或 Streamable HTTP
- **路由**：Hono 等轻量路由
- **资源**：静态 HTML/JS 通过 asset binding 提供

官方示例服务器：
- `threejs-server` — 3D 可视化
- `map-server` — 交互地图
- `pdf-server` — 文档查看
- `system-monitor-server` — 实时仪表盘

### 1.6 相关资源

- [SEP-1865 规范文档](https://modelcontextprotocol.io/seps/1865-mcp-apps-interactive-user-interfaces-for-mcp)
- [官方博客介绍](https://blog.modelcontextprotocol.io/posts/2025-11-21-mcp-apps/)
- [GitHub PR #1865](https://github.com/modelcontextprotocol/modelcontextprotocol/pull/1865)
- [ext-apps 规范源码](https://github.com/modelcontextprotocol/ext-apps/blob/main/specification/2026-01-26/apps.mdx)
- [示例服务器仓库](https://github.com/modelcontextprotocol/ext-apps)

---

## 第二部分：mcp-language-server 的 MCP Apps 使用场景分析

### 2.1 项目现有工具总览

| 工具 | 层级 | 功能 | 输出格式 |
|------|------|------|----------|
| `search` | L1/L2/L3 | 统一搜索入口（智能路由） | 纯文本分层结果 |
| `search_text` | L1 | ripgrep 文本搜索 | 纯文本匹配列表 |
| `search_ast` | L2 | tree-sitter AST 查询 | 纯文本节点列表 |
| `search_symbol` | L3 | LSP 符号搜索 | 纯文本符号列表 |
| `definition` | L3 | 符号定义查找 | 纯文本代码片段 |
| `references` | L3 | 符号引用查找 | 纯文本位置列表 |
| `callers` | L3 | 调用层级（上游） | 纯文本层级列表 |
| `callees` | L3 | 调用层级（下游） | 纯文本层级列表 |
| `diagnostics` | L3 | 诊断信息 | 纯文本错误列表 |
| `hover` | L3 | 悬停信息 | 纯文本类型/文档 |
| `rename_symbol` | L3 | 重命名符号 | 纯文本修改摘要 |
| `edit_file` | - | 文本编辑 | 纯文本结果 |
| `ripgrep` | L1 | 文本/正则搜索 | 纯文本匹配列表 |
| `treesitter_query` | L2 | CSP 查询 AST | 纯文本匹配列表 |
| `treesitter_ast` | L2 | AST 结构查看 | 纯文本树结构 |
| `find_struct_usage` | L2 | 结构体使用位置 | 纯文本位置列表 |
| `find_struct_definition` | L2 | 结构体定义位置 | 纯文本位置列表 |

**共同特征**：所有工具当前均为**纯文本输出**，适合 LLM 消费，但缺乏人机交互能力。

### 2.2 可使用 MCP Apps 增强的场景

#### 场景 1：调用层级可视化（callers / callees）

**现状**：`callers` 和 `callees` 返回纯文本列表，按深度分组：

```
=== Callers (depth 1-3, 15 total) ===

--- Depth 1 (3 functions) ---
  main at main.go:L42:C1
  init at main.go:L15:C1
  runServer at server.go:L88:C1

--- Depth 2 (7 functions) ---
  ...
```

**MCP Apps 增强**：
- 渲染**交互式调用关系图**（D3.js / Cytoscape.js）
- 节点可点击展开/折叠，显示函数签名
- 支持深度滑块实时调整
- 不同深度用不同颜色区分

**UI 资源注册**：
```json
{
  "uri": "ui://call-hierarchy/graph",
  "name": "Call Hierarchy Graph",
  "mimeType": "text/html;profile=mcp-app"
}
```

**工具关联**：
```json
{
  "name": "callers",
  "_meta": {
    "ui/resourceUri": "ui://call-hierarchy/graph",
    "ui/dataBinding": {
      "nodes": "callResults",
      "direction": "incoming"
    }
  }
}
```

**价值**：对于深度分析（depth=5~10），纯文本输出会淹没用户。可视化图表让调用链一目了然，特别适合安全检视中追踪漏洞传播路径。

---

#### 场景 2：诊断信息仪表盘（diagnostics）

**现状**：`diagnostics` 返回错误/警告列表：

```
src/main.cpp
Diagnostics in File: 5
ERROR at L42:C15: use of undeclared identifier 'buffer'
WARNING at L55:C3: unused variable 'count'
...
```

**MCP Apps 增强**：
- 渲染**文件诊断仪表盘**：
  - 左侧：文件树，有问题的文件高亮显示错误数徽章
  - 右侧：代码编辑器视图，带行内诊断标记（红/黄波浪线）
  - 底部：诊断详情面板，显示错误消息和修复建议
- 按严重程度过滤（Error/Warning/Info）
- 支持一键跳转到问题位置

**UI 资源注册**：
```json
{
  "uri": "ui://diagnostics/dashboard",
  "name": "Diagnostics Dashboard",
  "mimeType": "text/html;profile=mcp-app"
}
```

**价值**：对于大型 C/C++ 代码库（"百万级"），一次性查看多个文件诊断的仪表盘，比逐文件查看文本输出高效得多。安全检视中可快速定位高风险文件。

---

#### 场景 3：搜索结果交互式浏览器（search / search_text / search_ast / search_symbol）

**现状**：统一搜索返回三层结果：

```
=== [text layer] (23 results) ===
=== main.c ===
42: int getUserInput(char *buf)
...

=== [ast layer] (5 results) ===
...
```

**MCP Apps 增强**：
- 渲染**分层搜索结果浏览器**：
  - 顶部标签页切换 L1/L2/L3 结果
  - 左侧：搜索结果列表，显示文件名、行号、上下文预览
  - 右侧：代码预览面板，带语法高亮
  - 支持点击跳转、正则高亮匹配内容
- 对于 `search_auto` 模式，可视化显示路由决策（为什么选了某一层）

**UI 资源注册**：
```json
{
  "uri": "ui://search/results-browser",
  "name": "Search Results Browser",
  "mimeType": "text/html;profile=mcp-app"
}
```

**价值**：安全检视中常需要浏览大量搜索结果（如查找所有 `strcpy` 调用），交互式浏览器支持快速筛选和预览，减少 LLM 上下文消耗。

---

#### 场景 4：AST 可视化树（treesitter_ast）

**现状**：`treesitter_ast` 返回文本树：

```
translation_unit
  function_definition
    type: primitive_type "int"
    declarator: function_declarator
      name: identifier "main"
    body: compound_statement
      ...
```

**MCP Apps 增强**：
- 渲染**可折叠的 AST 树形图**：
  - 节点可展开/折叠
  - 点击节点高亮对应源代码行
  - 按节点类型着色（函数定义绿色、类型蓝色等）
  - 搜索过滤特定节点类型

**UI 资源注册**：
```json
{
  "uri": "ui://ast/tree-viewer",
  "name": "AST Tree Viewer",
  "mimeType": "text/html;profile=mcp-app"
}
```

**价值**：对于复杂的 C++ 模板代码，文本 AST 难以阅读。可视化树帮助理解嵌套结构，辅助安全检视中识别危险代码模式。

---

#### 场景 5：符号关系图（definition / references / find_struct_usage）

**现状**：`references` 返回位置列表，`definition` 返回代码片段，`find_struct_usage` 返回使用位置。

**MCP Apps 增强**：
- 渲染**符号关系图**：
  - 中心节点：目标符号（函数/结构体/变量）
  - 上游节点：定义位置
  - 下游节点：所有引用位置，按文件分组
  - 连线显示调用/使用关系
  - 节点可点击展开代码预览

**特别适用于 `find_struct_usage`**：
- 结构体 `UserData` 在 50+ 文件中被使用
- 可视化展示各使用点的上下文（变量声明、参数、返回类型等）

**UI 资源注册**：
```json
{
  "uri": "ui://symbol/relation-graph",
  "name": "Symbol Relation Graph",
  "mimeType": "text/html;profile=mcp-app"
}
```

**价值**：安全检视中分析结构体传播路径（如追踪敏感数据如何在代码中流动），纯文本列表不够直观。

---

#### 场景 6：代码编辑差异预览（edit_file）

**现状**：`edit_file` 直接应用编辑，返回成功/失败信息。

**MCP Apps 增强**：
- 渲染**差异预览面板**：
  - 左右分栏：原始代码 vs 修改后代码
  - 行级差异高亮（删除红色、新增绿色）
  - 用户确认后才执行实际修改
  - 支持多文件批量编辑预览

**UI 资源注册**：
```json
{
  "uri": "ui://editor/diff-preview",
  "name": "Edit Diff Preview",
  "mimeType": "text/html;profile=mcp-app"
}
```

**价值**：重命名符号（`rename_symbol`）可能影响数百个文件，差异预览让用户在提交前确认变更范围，避免意外修改。

---

#### 场景 7：工作区概览仪表盘

**当前项目没有此工具，但 MCP Apps 可支持新增**

**MCP Apps 增强**：
- 渲染**工作区概览仪表盘**：
  - 文件类型分布饼图
  - 代码行数统计
  - 诊断数量趋势图
  - 最热符号（被引用最多）排行榜
  - 最近修改文件列表

**新增工具**：`workspace_overview`

```json
{
  "name": "workspace_overview",
  "description": "Get an interactive overview of the workspace",
  "_meta": {
    "ui/resourceUri": "ui://workspace/overview"
  }
}
```

**价值**：在开始安全检视前，快速了解代码库规模和结构，帮助 LLM 制定检视策略。

---

### 2.3 场景优先级评估

| 优先级 | 场景 | 理由 |
|--------|------|------|
| **P0** | 调用层级可视化 | 深度调用链是纯文本的致命弱点，可视化收益最高 |
| **P0** | 诊断仪表盘 | 百万级代码库的诊断数量大，需要聚合视图 |
| **P1** | 搜索结果浏览器 | 三层搜索结果浏览是纯文本的低效场景 |
| **P1** | 符号关系图 | 结构体/函数引用关系复杂时需要图形辅助 |
| **P2** | AST 可视化 | 辅助功能，主要用于理解而非分析 |
| **P2** | 差异预览 | 增强安全性，减少误操作 |
| **P3** | 工作区概览 | 新增功能，可作为入口仪表盘 |

### 2.4 实施考量

#### 技术可行性

本项目基于 Go 标准库，MCP Apps 需要额外处理：

1. **UI 模板服务**：需要增加静态 HTML 模板文件和对应的资源路由
2. **数据绑定**：工具输出需要结构化（JSON）以便前端渲染
3. **SDK 升级**：`mcp-go` SDK 需支持扩展的 `_meta` 字段和 `ui://` 资源注册

#### 架构影响

```
当前架构（纯文本）:
MCP 客户端 -> tools.go -> 工具函数 -> 纯文本格式化 -> LLM

MCP Apps 增强后:
MCP 客户端 -> tools.go -> 工具函数 -> 结构化数据
                                    |
                                    +--> 纯文本格式化 -> LLM（保留）
                                    +--> JSON 数据 -> ui:// 模板 -> 交互式 UI
```

**关键改动**：
- 每个工具函数需要同时支持**文本输出**（LLM 消费）和**结构化输出**（UI 消费）
- 建议增加 `format` 参数：`text`（默认）或 `json`

#### 依赖评估

| 依赖 | 状态 | 说明 |
|------|------|------|
| `mcp-go` SDK | 需升级 | 当前版本可能不支持 `_meta` 和 `ui://` 资源 |
| 前端框架 | 新增 | 需引入轻量 HTML/CSS/JS（推荐 Vanilla JS + 少量库） |
| LSP 协议 | 不变 | 现有 LSP 通信不受影响 |

---

## 第三部分：下一步建议

1. **短期（概念验证）**：选择 `callers`/`callees` 或 `diagnostics` 作为首个 MCP Apps 增强目标，验证架构可行性
2. **中期**：逐步为 `search`、`references`、`treesitter_ast` 增加 UI 支持
3. **长期**：探索工作区概览仪表盘，作为安全检视的入口交互
4. **持续关注**：`mcp-go` SDK 对 MCP Apps 扩展的支持进展，等待上游合并后再大规模实施

---

## 附录：现有工具输出到结构化数据的映射

### callers / callees

```go
type CallHierarchyNode struct {
    Name       string              `json:"name"`
    FilePath   string              `json:"filePath"`
    Line       int                 `json:"line"`
    Column     int                 `json:"column"`
    Depth      int                 `json:"depth"`
    Children   []CallHierarchyNode `json:"children,omitempty"`
}

type CallHierarchyData struct {
    Direction string              `json:"direction"` // "incoming" | "outgoing"
    Root      CallHierarchyNode   `json:"root"`
    Total     int                 `json:"total"`
}
```

### diagnostics

```go
type DiagnosticItem struct {
    Severity    string `json:"severity"`    // "ERROR" | "WARNING" | "INFO" | "HINT"
    Line        int    `json:"line"`
    Column      int    `json:"column"`
    Message     string `json:"message"`
    Source      string `json:"source,omitempty"`
    Code        string `json:"code,omitempty"`
    FilePath    string `json:"filePath"`
}

type DiagnosticsData struct {
    FilePath     string           `json:"filePath"`
    Total        int              `json:"total"`
    ErrorCount   int              `json:"errorCount"`
    WarningCount int              `json:"warningCount"`
    Items        []DiagnosticItem `json:"items"`
}
```

### search results

```go
type SearchResultItem struct {
    Layer    string `json:"layer"`    // "text" | "ast" | "symbol"
    FilePath string `json:"filePath"`
    Line     int    `json:"line"`
    Column   int    `json:"column"`
    Content  string `json:"content"`
    Match    string `json:"match,omitempty"`
}

type SearchData struct {
    Query   string             `json:"query"`
    Results []SearchResultItem `json:"results"`
    Counts  map[string]int     `json:"counts"` // {"text": 23, "ast": 5, "symbol": 1}
}
```
