package treesitter

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

type QueryResult struct {
	Node     *sitter.Node
	Capture  string
	Content  string
	FilePath string
	Line     uint32
	Column   uint32
}

func RunQuery(tree *sitter.Tree, source []byte, lang *sitter.Language, pattern string) ([]QueryResult, error) {
	q, err := sitter.NewQuery([]byte(pattern), lang)
	if err != nil {
		return nil, fmt.Errorf("failed to compile query: %w", err)
	}
	defer q.Close()

	qc := sitter.NewQueryCursor()
	defer qc.Close()

	qc.Exec(q, tree.RootNode())

	var results []QueryResult
	for {
		m, ok := qc.NextMatch()
		if !ok {
			break
		}

		m = qc.FilterPredicates(m, source)
		for _, c := range m.Captures {
			start := c.Node.StartPoint()
			content := c.Node.Content(source)
			results = append(results, QueryResult{
				Node:     c.Node,
				Capture:  q.CaptureNameForId(c.Index),
				Content:  content,
				Line:     start.Row + 1,
				Column:   start.Column + 1,
			})
		}
	}

	return results, nil
}

func QueryDirectory(dir, pattern, language string) ([]QueryResult, error) {
	parser := NewParser(language)
	defer parser.Close()

	var results []QueryResult

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		if !isSourceFile(ext) {
			return nil
		}

		tree, source, err := parser.ParseFile(context.Background(), path)
		if err != nil {
			return nil
		}
		defer tree.Close()

		queryResults, err := RunQuery(tree, source, parser.Language(), pattern)
		if err != nil {
			return nil
		}

		for i := range queryResults {
			queryResults[i].FilePath = path
		}
		results = append(results, queryResults...)

		return nil
	})

	return results, err
}

func isSourceFile(ext string) bool {
	switch ext {
	case ".c", ".h", ".cpp", ".cxx", ".cc", ".hpp", ".hxx":
		return true
	default:
		return false
	}
}
