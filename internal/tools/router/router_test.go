package router

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSearchAllUnifiedPipeline(t *testing.T) {
	dir := t.TempDir()
	src := "int main(void) {\n\t// TODO: fix this\n\treturn 0;\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "main.c"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	r := NewRouter(dir) // no LSP client: symbol layer must fall back
	results, err := r.Search(context.Background(), SearchOptions{Query: "TODO", Strategy: "auto"})
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected a single unified result, got %d", len(results))
	}
	if results[0].Layer != "unified" {
		t.Errorf("expected unified layer, got %q", results[0].Layer)
	}

	content := results[0].Content
	for _, want := range []string{
		"WARNING: LSP unavailable, results are plain text matches",
		"=== [unified]",
		"TODO: fix this",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("output missing %q, got:\n%s", want, content)
		}
	}

	// rg layer and lsp-fallback rg layer find the same line; it must be
	// deduplicated to a single occurrence.
	if n := strings.Count(content, "TODO: fix this"); n != 1 {
		t.Errorf("expected deduplicated hit, found %d occurrences in:\n%s", n, content)
	}
}

func TestSearchAllNoResults(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.c"), []byte("int x;\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := NewRouter(dir)
	results, err := r.Search(context.Background(), SearchOptions{Query: "zzz_no_such_token_zzz", Strategy: "auto"})
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(results) != 1 || !strings.Contains(results[0].Content, "No results found") {
		t.Errorf("expected 'No results found', got %+v", results)
	}
}

func TestByteOffsetOfLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.c")
	content := []byte("ab\ncde\nf\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	cache := map[string][]byte{}
	cases := map[int]int{0: 0, 1: 3, 2: 7, 3: 9, 4: -1}
	for line, want := range cases {
		if got := byteOffsetOfLine(path, line, cache); got != want {
			t.Errorf("line %d: expected offset %d, got %d", line, want, got)
		}
	}

	if got := byteOffsetOfLine(filepath.Join(dir, "missing.c"), 0, cache); got != -1 {
		t.Errorf("missing file: expected -1, got %d", got)
	}
}
