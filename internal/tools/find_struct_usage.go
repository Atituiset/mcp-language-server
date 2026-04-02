package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/isaacphi/mcp-language-server/internal/tools/treesitter"
)

// FindStructUsage finds all usages of a struct type in C/C++ code
func FindStructUsage(ctx context.Context, workspaceDir, structName, filePath, language string) (string, error) {
	if language == "" {
		language = "cpp"
	}

	// Build a tree-sitter query to find the struct type usage
	// This matches type_identifier nodes that match the struct name
	query := fmt.Sprintf(`((type_identifier) @type (#eq? @type "%s"))`, structName)

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
		return fmt.Sprintf("No usages of struct '%s' found", structName), nil
	}

	return formatStructUsageResults(results, structName), nil
}

// FindStructDefinition finds the definition of a struct
func FindStructDefinition(ctx context.Context, workspaceDir, structName, filePath, language string) (string, error) {
	if language == "" {
		language = "cpp"
	}

	// Query for struct_specifier with the struct name
	query := fmt.Sprintf(`(struct_specifier name: (type_identifier) @name (#eq? @name "%s")) @struct`, structName)

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
		return fmt.Sprintf("No definition of struct '%s' found", structName), nil
	}

	return formatStructDefinitionResults(results, structName), nil
}

func formatStructUsageResults(results []treesitter.QueryResult, structName string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("=== Usages of struct '%s' (%d locations) ===\n\n", structName, len(results)))

	currentFile := ""
	for _, r := range results {
		if r.FilePath != currentFile {
			b.WriteString(fmt.Sprintf("\n--- %s ---\n", r.FilePath))
			currentFile = r.FilePath
		}
		b.WriteString(fmt.Sprintf("  Line %d, Col %d: %s\n", r.Line, r.Column, truncate(r.Content, 60)))
	}

	return b.String()
}

func formatStructDefinitionResults(results []treesitter.QueryResult, structName string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("=== Definition of struct '%s' (%d locations) ===\n\n", structName, len(results)))

	for _, r := range results {
		b.WriteString(fmt.Sprintf("File: %s\n", r.FilePath))
		b.WriteString(fmt.Sprintf("Location: Line %d, Col %d\n", r.Line, r.Column))
		b.WriteString(fmt.Sprintf("Content: %s\n\n", truncate(r.Content, 100)))
	}

	return b.String()
}
