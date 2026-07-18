package treesitter

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	sitter "github.com/smacker/go-tree-sitter"
)

type QueryResult struct {
	Node     *sitter.Node
	Capture  string
	NodeType string
	Content  string
	FilePath string
	Line     uint32
	Column   uint32
	// Byte offsets of the node in the source file, captured while the
	// parse tree is still alive (Node pointers dangle after tree.Close).
	StartByte uint32
	EndByte   uint32
}

func RunQuery(tree *sitter.Tree, source []byte, lang *sitter.Language, pattern string) ([]QueryResult, error) {
	q, err := sitter.NewQuery([]byte(pattern), lang)
	if err != nil {
		return nil, fmt.Errorf("failed to compile query: %w", err)
	}
	defer q.Close()

	return runCompiledQuery(q, tree, source), nil
}

// runCompiledQuery executes an already-compiled query against one tree.
func runCompiledQuery(q *sitter.Query, tree *sitter.Tree, source []byte) []QueryResult {
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
				Node:      c.Node,
				Capture:   q.CaptureNameForId(c.Index),
				NodeType:  c.Node.Type(),
				Content:   content,
				Line:      start.Row + 1,
				Column:    start.Column + 1,
				StartByte: c.Node.StartByte(),
				EndByte:   c.Node.EndByte(),
			})
		}
	}

	return results
}

// QueryDirectory runs a CSP query over every C/C++ source file under dir.
// Files are parsed and queried concurrently (one parser per worker); the
// pattern is compiled once up front so invalid queries fail fast instead of
// after a full workspace scan. Per-file parse errors are skipped. Result
// order is nondeterministic.
func QueryDirectory(dir, pattern, language string) ([]QueryResult, error) {
	var files []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if isSourceFile(strings.ToLower(filepath.Ext(path))) {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, nil
	}

	// Fail fast on invalid patterns before parsing anything.
	probe := NewParser(language)
	_, err = sitter.NewQuery([]byte(pattern), probe.Language())
	probe.Close()
	if err != nil {
		return nil, fmt.Errorf("failed to compile query: %w", err)
	}

	workers := runtime.NumCPU()
	if len(files) < workers {
		workers = len(files)
	}

	fileCh := make(chan string)
	resCh := make(chan []QueryResult, workers)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			parser := NewParser(language)
			defer parser.Close()
			q, err := sitter.NewQuery([]byte(pattern), parser.Language())
			if err != nil {
				return
			}
			defer q.Close()

			for path := range fileCh {
				tree, source, err := parser.ParseFile(context.Background(), path)
				if err != nil {
					continue
				}
				qr := runCompiledQuery(q, tree, source)
				tree.Close()
				if len(qr) == 0 {
					continue
				}
				for i := range qr {
					qr[i].FilePath = path
				}
				resCh <- qr
			}
		}()
	}
	go func() {
		for _, f := range files {
			fileCh <- f
		}
		close(fileCh)
	}()
	go func() {
		wg.Wait()
		close(resCh)
	}()

	var results []QueryResult
	for qr := range resCh {
		results = append(results, qr...)
	}
	return results, nil
}

func isSourceFile(ext string) bool {
	switch ext {
	case ".c", ".h", ".cpp", ".cxx", ".cc", ".hpp", ".hxx":
		return true
	default:
		return false
	}
}
