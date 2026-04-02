package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/isaacphi/mcp-language-server/internal/tools/treesitter"
)

// FindStructUsage 查找结构体的所有使用位置
//
// 使用 tree-sitter 查询 C/C++ 代码中指定结构体类型的所有使用位置，
// 包括变量声明、函数参数、返回类型、指针引用等。
//
// 参数:
//   - ctx: 上下文
//   - workspaceDir: 工作区目录
//   - structName: 结构体名称
//   - filePath: 可选，限定搜索的文件路径
//   - language: 可选，语言类型（"c" 或 "cpp"，默认 "cpp"）
//
// 返回: 格式化的结构体使用位置列表
func FindStructUsage(ctx context.Context, workspaceDir, structName, filePath, language string) (string, error) {
	if language == "" {
		language = "cpp"
	}

	// 构建 tree-sitter 查询，匹配指定名称的类型标识符
	query := fmt.Sprintf(`((type_identifier) @type (#eq? @type "%s"))`, structName)

	var results []treesitter.QueryResult
	var err error

	if filePath != "" {
		// 在指定文件中搜索
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
		// 在整个工作区中搜索
		results, err = treesitter.QueryDirectory(workspaceDir, query, language)
		if err != nil {
			return "", fmt.Errorf("directory query failed: %w", err)
		}
	}

	if len(results) == 0 {
		return fmt.Sprintf("No usages of struct '%s' found", structName), nil
	}

	return formatStructUsageResults(results, structName), nil
}

// FindStructDefinition 查找结构体的定义位置
//
// 使用 tree-sitter 查询 C/C++ 代码中指定结构体的定义。
//
// 参数:
//   - ctx: 上下文
//   - workspaceDir: 工作区目录
//   - structName: 结构体名称
//   - filePath: 可选，限定搜索的文件路径
//   - language: 可选，语言类型（"c" 或 "cpp"，默认 "cpp"）
//
// 返回: 格式化的结构体定义位置
func FindStructDefinition(ctx context.Context, workspaceDir, structName, filePath, language string) (string, error) {
	if language == "" {
		language = "cpp"
	}

	// 查询结构体定义
	query := fmt.Sprintf(`(struct_specifier name: (type_identifier) @name (#eq? @name "%s")) @struct`, structName)

	var results []treesitter.QueryResult
	var err error

	if filePath != "" {
		// 在指定文件中搜索
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
		// 在整个工作区中搜索
		results, err = treesitter.QueryDirectory(workspaceDir, query, language)
		if err != nil {
			return "", fmt.Errorf("directory query failed: %w", err)
		}
	}

	if len(results) == 0 {
		return fmt.Sprintf("No definition of struct '%s' found", structName), nil
	}

	return formatStructDefinitionResults(results, structName), nil
}

// formatStructUsageResults 格式化结构体使用结果，按文件分组输出
//
// 参数:
//   - results: 查询结果列表
//   - structName: 结构体名称
//
// 返回: 格式化的字符串
func formatStructUsageResults(results []treesitter.QueryResult, structName string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("=== Usages of struct '%s' (%d locations) ===\n\n", structName, len(results)))

	currentFile := ""
	for _, r := range results {
		if r.FilePath != currentFile {
			b.WriteString(fmt.Sprintf("\n--- %s ---\n", r.FilePath))
			currentFile = r.FilePath
		}
		b.WriteString(fmt.Sprintf("  Line %d, Col %d: %s\n", r.Line, r.Column, truncateStr(r.Content, 60)))
	}

	return b.String()
}

// formatStructDefinitionResults 格式化结构体定义结果
//
// 参数:
//   - results: 查询结果列表
//   - structName: 结构体名称
//
// 返回: 格式化的字符串
func formatStructDefinitionResults(results []treesitter.QueryResult, structName string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("=== Definition of struct '%s' (%d locations) ===\n\n", structName, len(results)))

	for _, r := range results {
		b.WriteString(fmt.Sprintf("File: %s\n", r.FilePath))
		b.WriteString(fmt.Sprintf("Location: Line %d, Col %d\n", r.Line, r.Column))
		b.WriteString(fmt.Sprintf("Content: %s\n\n", truncateStr(r.Content, 100)))
	}

	return b.String()
}
