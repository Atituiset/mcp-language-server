package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/isaacphi/mcp-language-server/internal/tools/treesitter"
)

func RunTreesitterQuery(ctx context.Context, workspaceDir, query, filePath, language string) (string, error) {
	if language == "" {
		language = "cpp"
	}

	var results []treesitter.QueryResult
	var err error

	if filePath != "" {
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
		b.WriteString(fmt.Sprintf("  [%s] %s (L%d:C%d)\n", r.Capture, truncate(r.Content, 60), r.Line, r.Column))
	}

	return b.String(), nil
}

func GetAST(ctx context.Context, filePath string, nodeType string, maxDepth int) (string, error) {
	if maxDepth <= 0 {
		maxDepth = 10
	}

	lang := treesitter.DetectLanguage(filePath)
	parser := treesitter.NewParser(lang)
	defer parser.Close()

	tree, source, err := parser.ParseFile(ctx, filePath)
	if err != nil {
		return "", fmt.Errorf("failed to parse file: %w", err)
	}
	defer tree.Close()

	ast := treesitter.TreeToAST(tree, source, maxDepth)

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
				truncate(n.Content, 80)))
		}
		return b.String(), nil
	}

	return ast.String(), nil
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
