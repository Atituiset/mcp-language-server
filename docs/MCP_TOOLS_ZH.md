# MCP Language Server 工具文档

## 概述

MCP Language Server 通过 JSON-RPC 2.0 协议提供代码上下文提取能力，支持 L1（文本）、L2（AST）、L3（符号）三层搜索架构。

---

## 协议格式

### 请求格式

```
Content-Length: <字节数>\r\n\r\n<JSON>
```

**JSON-RPC 请求结构**：
```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "tools/call",
  "params": {
    "name": "<工具名>",
    "arguments": { /* 工具参数 */ }
  }
}
```

### 响应格式

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "content": [
      {
        "type": "text",
        "text": "<结果文本>"
      }
    ]
  }
}
```

### 错误响应

```json
{
  "jsonrpc": "2.0",
  "id": null,
  "error": {
    "code": -32700,
    "message": "Parse error"
  }
}
```

---

## 工具列表

### 1. search - 统一搜索（推荐）

智能路由的统一搜索入口，自动选择最佳搜索层。

**参数**：
| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `query` | string | 是 | 搜索内容 |
| `strategy` | string | 否 | 搜索策略：`auto`（默认）、`text`、`ast`、`symbol` |
| `intent` | string | 否 | 意图提示：`todo`、`function`、`struct`、`definition`、`reference`、`type` |
| `filePath` | string | 否 | 限制搜索到特定文件 |
| `language` | string | 否 | 语言：`c` 或 `cpp`（仅用于 AST 搜索） |

**请求示例**：
```bash
REQ='{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search","arguments":{"query":"helperFunction","strategy":"auto"}}}'
echo -e "Content-Length: ${#REQ}\r\n\r\n$REQ" | ./mcp-language-server --workspace /path/to/project --lsp clangd
```

**测试结果**：
```
=== [text layer] (13 results) ===
=== [symbol layer] (13 results) ===
=== [ast layer] (1 results) ===
```

---

### 2. search_text - L1 文本搜索

使用 ripgrep 进行快速文本搜索。

**参数**：
| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `query` | string | 是 | 搜索文本或正则表达式 |
| `filePath` | string | 否 | 限制到特定文件 |

**请求示例**：
```bash
REQ='{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"search_text","arguments":{"query":"helper"}}}'
```

**测试结果**：
```
=== [text layer] (20 results) ===
```

---

### 3. search_ast - L2 AST 查询

使用 tree-sitter 进行 AST 结构查询。

**参数**：
| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `query` | string | 是 | CSP 查询模式 |
| `filePath` | string | 否 | 限制到特定文件 |
| `language` | string | 否 | 语言：`c` 或 `cpp` |

**CSP 查询示例**：
```scheme
(function_definition) @func                           # 查找所有函数定义
((identifier) @name (#eq? @name "main"))             # 查找名为 main 的标识符
(struct_specifier) @struct                           # 查找结构体定义
```

**请求示例**：
```bash
REQ='{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"search_ast","arguments":{"query":"(function_definition) @func","filePath":"src/main.cpp"}}}'
```

**测试结果**：
```
Found 2 matches:
=== src/main.cpp ===
  [func] void foo_bar() { ... (L5:C1)
  [func] int main() { ... (L10:C1)
```

---

### 4. search_symbol - L3 符号搜索

使用 LSP 进行语义符号搜索。

**参数**：
| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `query` | string | 是 | 符号名称 |
| `filePath` | string | 否 | 限制到特定文件 |

**请求示例**：
```bash
REQ='{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"search_symbol","arguments":{"query":"helperFunction"}}}'
```

**测试结果**：
```
=== [symbol layer] (13 results) ===
```

---

### 5. ripgrep - 快速文本搜索

直接使用 ripgrep 进行文本/正则搜索。

**参数**：
| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `pattern` | string | 是 | 正则表达式模式 |
| `include` | string | 否 | 文件 glob 模式（如 `*.go`） |
| `fileType` | string | 否 | 文件类型（如 `go`、`py`） |
| `maxCount` | number | 否 | 每个文件最大匹配数（默认 100） |
| `caseSensitive` | boolean | 否 | 是否大小写敏感（默认 false） |
| `wholeWord` | boolean | 否 | 是否全词匹配（默认 false） |
| `contextLines` | number | 否 | 上下文字行数（默认 0） |

**请求示例**：
```bash
REQ='{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"ripgrep","arguments":{"pattern":"helperFunction","maxCount":5}}}'
```

**测试结果**：
```
0: 0: ...
```

---

### 6. definition - 查找符号定义

读取符号（函数、类型、常量等）的源代码定义。

**参数**：
| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `symbolName` | string | 是 | 符号名称（如 `MyFunction`、`MyType.MyMethod`） |

**请求示例**：
```bash
REQ='{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"definition","arguments":{"symbolName":"helperFunction"}}}'
```

**测试结果**：
```
helperFunction not found
```

---

### 7. references - 查找引用

查找符号的所有引用位置。

**参数**：
| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `symbolName` | string | 是 | 符号名称 |

**请求示例**：
```bash
REQ='{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"references","arguments":{"symbolName":"helperFunction"}}}'
```

**测试结果**：
```
No references found for symbol: helperFunction
```

---

### 8. callers - 查找调用者

查找调用指定函数的函数（入向调用层级）。

**参数**：
| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `filePath` | string | 是 | 包含函数的文件路径 |
| `line` | number | 是 | 行号（1-based） |
| `column` | number | 是 | 列号（1-based） |
| `depth` | number | 否 | 最大遍历深度（默认 1，最大 10） |

**请求示例**：
```bash
REQ='{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"callers","arguments":{"filePath":"src/main.cpp","line":11,"column":1}}}'
```

**测试结果**：
```
No call hierarchy items found at this position
```

---

### 9. callees - 查找被调用者

查找指定函数调用的函数（出向调用层级）。

**参数**：
| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `filePath` | string | 是 | 包含函数的文件路径 |
| `line` | number | 是 | 行号（1-based） |
| `column` | number | 是 | 列号（1-based） |
| `depth` | number | 否 | 最大遍历深度（默认 1，最大 10） |

**请求示例**：
```bash
REQ='{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"callees","arguments":{"filePath":"src/main.cpp","line":11,"column":1}}}'
```

**测试结果**：
```
No call hierarchy items found at this position
```

---

### 10. hover - 悬停信息

获取符号的悬停信息（类型、文档）。

**参数**：
| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `filePath` | string | 是 | 文件路径 |
| `line` | number | 是 | 行号（1-based） |
| `column` | number | 是 | 列号（1-based） |

**请求示例**：
```bash
REQ='{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"hover","arguments":{"filePath":"src/main.cpp","line":5,"column":6}}}'
```

**测试结果**：
```
function foo_bar
→ void
FooBar is a simple function for testing
void foo_bar()
```

---

### 11. rename_symbol - 重命名符号

重命名符号并更新所有引用。

**参数**：
| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `filePath` | string | 是 | 文件路径 |
| `line` | number | 是 | 行号（1-based） |
| `column` | number | 是 | 列号（1-based） |
| `newName` | string | 是 | 新名称 |

**请求示例**：
```bash
REQ='{"jsonrpc":"2.0","id":11,"method":"tools/call","params":{"name":"rename_symbol","arguments":{"filePath":"src/main.cpp","line":5,"column":6,"newName":"foo_bar_renamed"}}}'
```

**测试结果**：
```
failed to rename symbol: failed to apply changes: ... (路径问题)
```

---

### 12. diagnostics - 诊断信息

获取文件的诊断信息（错误、警告等）。

**参数**：
| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `filePath` | string | 是 | 文件路径 |
| `contextLines` | boolean | 否 | 是否包含上下文行（默认 false） |
| `showLineNumbers` | boolean | 否 | 是否显示行号（默认 true） |

**请求示例**：
```bash
REQ='{"jsonrpc":"2.0","id":12,"method":"tools/call","params":{"name":"diagnostics","arguments":{"filePath":"src/main.cpp"}}}'
```

**测试结果**：
```
No diagnostics found for src/main.cpp
```

---

### 13. edit_file - 文本编辑

对文件应用多重文本编辑。

**参数**：
| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `filePath` | string | 是 | 文件路径 |
| `edits` | array | 是 | 编辑列表 |

**edits 数组元素**：
| 字段 | 类型 | 说明 |
|------|------|------|
| `startLine` | number | 起始行（包含，1-based） |
| `endLine` | number | 结束行（包含，1-based） |
| `newText` | string | 替换文本（空字符串表示删除） |

**请求示例**：
```bash
REQ='{"jsonrpc":"2.0","id":13,"method":"tools/call","params":{"name":"edit_file","arguments":{"filePath":"src/main.cpp","edits":[{"startLine":5,"endLine":5,"newText":"// Edited line 5"}]}}}'
```

**测试结果**：
```
Successfully applied text edits. 1 lines removed, 1 lines added.
```

---

### 14. find_struct_definition - 查找结构体定义

查找 C/C++ 结构体的定义位置。

**参数**：
| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `structName` | string | 是 | 结构体名称 |
| `filePath` | string | 否 | 限制到特定文件 |
| `language` | string | 否 | 语言：`c` 或 `cpp`（默认 cpp） |

**请求示例**：
```bash
REQ='{"jsonrpc":"2.0","id":14,"method":"tools/call","params":{"name":"find_struct_definition","arguments":{"structName":"TestStruct"}}}'
```

**测试结果**：
```
=== Definition of struct 'TestStruct' (2 locations) ===
File: src/types.cpp
Location: Line 6, Col 1
Content: struct TestStruct { int value; }
```

---

### 15. find_struct_usage - 查找结构体使用

查找 C/C++ 结构体的所有使用位置。

**参数**：
| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `structName` | string | 是 | 结构体名称 |
| `filePath` | string | 否 | 限制到特定文件 |
| `language` | string | 否 | 语言：`c` 或 `cpp`（默认 cpp） |

**请求示例**：
```bash
REQ='{"jsonrpc":"2.0","id":15,"method":"tools/call","params":{"name":"find_struct_usage","arguments":{"structName":"TestStruct"}}}'
```

**测试结果**：
```
=== Usages of struct 'TestStruct' (1 locations) ===
/src/types.cpp Line 6, Col 8: TestStruct
```

---

### 16. treesitter_ast - 查看 AST 结构

获取 C/C++ 文件的 AST 结构。

**参数**：
| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `filePath` | string | 是 | 文件路径 |
| `maxDepth` | number | 否 | 最大遍历深度（默认 10） |
| `nodeType` | string | 否 | 过滤显示特定节点类型 |

**请求示例**：
```bash
REQ='{"jsonrpc":"2.0","id":16,"method":"tools/call","params":{"name":"treesitter_ast","arguments":{"filePath":"src/main.cpp","maxDepth":3}}}'
```

**测试结果**：
```
[translation_unit] "#include <iostream>\n..." (L1:C1 - L18:C2)
  [preproc_include] "#include <iostream>\n" (L1:C1 - L2:C1)
  [preproc_include] "#include \"helper.hpp\"\n" (L2:C1 - L3:C1)
  [function_definition] "void foo_bar() {..." (L5:C1 - L8:C2)
  [function_definition] "int main() {..." (L10:C1 - L18:C2)
```

---

### 17. treesitter_query - AST 查询

使用 CSP 模式查询 AST。

**参数**：
| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `query` | string | 是 | CSP 查询模式 |
| `filePath` | string | 否 | 限制到特定文件 |
| `language` | string | 否 | 语言：`c` 或 `cpp` |

**请求示例**：
```bash
REQ='{"jsonrpc":"2.0","id":17,"method":"tools/call","params":{"name":"treesitter_query","arguments":{"query":"(function_definition) @func","filePath":"src/main.cpp"}}}'
```

---

## 三层搜索架构

| 层级 | 工具 | 速度 | 用途 |
|------|------|------|------|
| **L1** | `search_text`, `ripgrep` | 最快 | 文本/正则搜索、TODO、注释 |
| **L2** | `search_ast`, `treesitter_query`, `find_struct_*` | 快 | AST 结构查询、CSP 模式 |
| **L3** | `search_symbol`, `definition`, `references`, `callers`, `callees` | 较慢 | 语义理解、符号定义、调用层级 |

### 智能路由规则

当 `strategy="auto"` 时：
- `intent` 包含 `todo`/`comment`/`string` → L1
- `intent` 包含 `function`/`struct`/`class` → L2
- `intent` 包含 `definition`/`reference`/`type` → L3
- 无 `intent` → 并行搜索所有层

---

## 使用示例

### 启动服务器

```bash
./mcp-language-server --workspace /path/to/project --lsp clangd
```

### 完整请求示例

```bash
# 1. 列出所有工具
REQ='{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}'
echo -e "Content-Length: ${#REQ}\r\n\r\n$REQ" | ./mcp-language-server --workspace . --lsp clangd

# 2. ripgrep 文本搜索
REQ='{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"ripgrep","arguments":{"pattern":"helperFunction","maxCount":5}}}'
echo -e "Content-Length: ${#REQ}\r\n\r\n$REQ" | ./mcp-language-server --workspace . --lsp clangd

# 3. 统一搜索（自动路由）
REQ='{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"search","arguments":{"query":"helperFunction","strategy":"auto"}}}'
echo -e "Content-Length: ${#REQ}\r\n\r\n$REQ" | ./mcp-language-server --workspace . --lsp clangd

# 4. treesitter_query AST 查询
REQ='{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"treesitter_query","arguments":{"query":"(function_definition) @func","filePath":"src/main.cpp"}}}'
echo -e "Content-Length: ${#REQ}\r\n\r\n$REQ" | ./mcp-language-server --workspace . --lsp clangd
```

### Python 调用示例

```python
import subprocess
import json

def call_mcp_tool(tool_name, arguments):
    request = {
        "jsonrpc": "2.0",
        "id": 1,
        "method": "tools/call",
        "params": {
            "name": tool_name,
            "arguments": arguments
        }
    }

    proc = subprocess.Popen(
        ["./mcp-language-server", "--workspace", "/path/to/project", "--lsp", "clangd"],
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE
    )

    content = json.dumps(request)
    header = f"Content-Length: {len(content)}\r\n\r\n"

    proc.stdin.write(header.encode() + content.encode())
    proc.stdin.flush()
    proc.stdin.close()

    import time
    time.sleep(3)

    output = proc.stdout.read()
    print(output.decode('utf-8', errors='replace'))
    proc.wait()

# 使用示例
call_mcp_tool("ripgrep", {"pattern": "class Node", "include": "*.cpp", "maxCount": 10})
```

---

## 支持的 LSP 服务器

| 语言 | LSP 服务器 | 配置示例 |
|------|------------|----------|
| Go | gopls | `--lsp gopls` |
| C/C++ | clangd | `--lsp clangd` |
| Rust | rust-analyzer | `--lsp rust-analyzer` |
| Python | pyright | `--lsp pyright` |
| TypeScript | typescript-language-server | `--lsp typescript-language-server` |
