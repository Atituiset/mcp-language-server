package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/isaacphi/mcp-language-server/internal/tools/treesitter"
)

// TreesitterOptions tree-sitter 搜索选项（保留用于扩展）
type TreesitterOptions struct {
	Language  string // 语言类型: "c" 或 "cpp"
	FilePath  string // 文件路径
	NodeType  string // 节点类型过滤
	MaxDepth  int    // 最大遍历深度
}

// RunTreesitterQuery 执行 tree-sitter CSP 查询
//
// 使用 tree-sitter 查询语言在指定文件或目录中搜索匹配的 AST 节点。
//
// 参数:
//   - ctx: 上下文
//   - workspaceDir: 工作区目录
//   - query: CSP 查询模式
//   - filePath: 可选，限定搜索的文件路径
//   - language: 可选，语言类型（默认 "cpp"）
//
// 返回: 格式化的查询结果
func RunTreesitterQuery(ctx context.Context, workspaceDir, query, filePath, language string) (string, error) {
	if language == "" {
		language = "cpp"
	}

	var results []treesitter.QueryResult
	var err error

	if filePath != "" {
		// 在指定文件中查询
		parser := treesitter.NewParser(language)
		defer parser.Close()

		tree, source, parseErr := parser.ParseFile(ctx, filePath)
		if parseErr != nil {
			return "", fmt.Errorf("failed to parse file: %w", parseErr)
		}
		defer tree.Close()

		results, err = treesitter.RunQuery(tree, source, parser.Language(), query)
		if err != nil {
			return "", fmt.Errorf("query failed: %w", err)
		}

		for i := range results {
			results[i].FilePath = filePath
		}
	} else {
		// 在整个工作区中查询
		results, err = treesitter.QueryDirectory(workspaceDir, query, language)
		if err != nil {
			return "", fmt.Errorf("directory query failed: %w", err)
		}
	}

	if len(results) == 0 {
		return "No matches found", nil
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("Found %d matches:\n\n", len(results)))

	currentFile := ""
	for _, r := range results {
		if r.FilePath != currentFile {
			b.WriteString(fmt.Sprintf("=== %s ===\n", r.FilePath))
			currentFile = r.FilePath
		}
		b.WriteString(fmt.Sprintf("  [%s] %s (L%d:C%d)\n", r.Capture, truncateStr(r.Content, 60), r.Line, r.Column))
	}

	return b.String(), nil
}

// GetAST 获取文件的 AST 结构
//
// 解析指定文件并返回其抽象语法树结构，可选择按节点类型过滤。
//
// 参数:
//   - ctx: 上下文
//   - filePath: 文件路径
//   - nodeType: 可选，按节点类型过滤
//   - maxDepth: 最大遍历深度（默认 10）
//
// 返回: 格式化的 AST 结构
func GetAST(ctx context.Context, filePath string, nodeType string, maxDepth int) (string, error) {
	if maxDepth <= 0 {
		maxDepth = 10
	}

	// 根据文件扩展名检测语言
	lang := treesitter.DetectLanguage(filePath)
	parser := treesitter.NewParser(lang)
	defer parser.Close()

	tree, source, err := parser.ParseFile(ctx, filePath)
	if err != nil {
		return "", fmt.Errorf("failed to parse file: %w", err)
	}
	defer tree.Close()

	// 转换为 AST 结构
	ast := treesitter.TreeToAST(tree, source, maxDepth)

	// 如果指定了节点类型，只返回匹配的节点
	if nodeType != "" {
		nodes := treesitter.FilterByType(ast, nodeType)
		if len(nodes) == 0 {
			return fmt.Sprintf("No nodes of type %q found", nodeType), nil
		}
		var b strings.Builder
		b.WriteString(fmt.Sprintf("Found %d nodes of type %q:\n\n", len(nodes), nodeType))
		for _, n := range nodes {
			b.WriteString(fmt.Sprintf("  %s (L%d:C%d - L%d:C%d)\n    %q\n",
				n.Type, n.StartRow+1, n.StartCol+1, n.EndRow+1, n.EndCol+1,
				truncateStr(n.Content, 80)))
		}
		return b.String(), nil
	}

	return ast.String(), nil
}

// truncateStr 截断字符串到指定长度
func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
