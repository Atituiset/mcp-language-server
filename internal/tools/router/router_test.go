package router

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isaacphi/mcp-language-server/internal/tools"
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

func TestSearchAutoIntentRoutedUnified(t *testing.T) {
	dir := t.TempDir()
	src := "int main(void) {\n\t// TODO: fix this\n\treturn 0;\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "main.c"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	r := NewRouter(dir)
	results, err := r.Search(context.Background(), SearchOptions{
		Query:    "TODO",
		Strategy: "auto",
		Intent:   "todo",
	})
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected a single result, got %d", len(results))
	}
	if results[0].Layer != "unified-text" {
		t.Errorf("expected unified-text layer, got %q", results[0].Layer)
	}
	content := results[0].Content
	if !strings.Contains(content, "=== [unified-text]") {
		t.Errorf("expected unified stats header, got:\n%s", content)
	}
	if !strings.Contains(content, "TODO: fix this") {
		t.Errorf("expected the TODO hit, got:\n%s", content)
	}
}

func TestSearchSymbolFallbackScopedByIncludeMap(t *testing.T) {
	dir := t.TempDir()

	// board_a files share a discriminating include dir (-IboardA);
	// board_b has its own unrelated include dir.
	cc := `[
  {"directory": "` + dir + `", "file": "` + filepath.Join(dir, "board_a/x.c") + `", "command": "gcc -Iinc -IboardA -c board_a/x.c"},
  {"directory": "` + dir + `", "file": "` + filepath.Join(dir, "board_a/x2.c") + `", "command": "gcc -Iinc -IboardA -c board_a/x2.c"},
  {"directory": "` + dir + `", "file": "` + filepath.Join(dir, "board_b/y.c") + `", "command": "gcc -IboardB -c board_b/y.c"}
]`
	if err := os.WriteFile(filepath.Join(dir, "compile_commands.json"), []byte(cc), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"board_a/x.c", "board_a/x2.c", "board_b/y.c"} {
		p := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("int secret_token;\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	r := NewRouter(dir) // no LSP client: symbol layer must fall back
	results, err := r.Search(context.Background(), SearchOptions{
		Query:    "secret_token",
		Strategy: "symbol",
		FilePath: filepath.Join(dir, "board_a", "x.c"),
	})
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(results) != 1 || results[0].Layer != "symbol-fallback-text" {
		t.Fatalf("expected symbol-fallback-text layer, got %+v", results)
	}

	content := results[0].Content
	if !strings.Contains(content, "(scoped to 2 files via include map)") {
		t.Errorf("expected include-map scope note, got:\n%s", content)
	}
	if !strings.Contains(content, "board_a/x.c") || !strings.Contains(content, "board_a/x2.c") {
		t.Errorf("expected hits in both board_a files, got:\n%s", content)
	}
	if strings.Contains(content, "board_b") {
		t.Errorf("out-of-neighborhood file leaked into scoped results:\n%s", content)
	}
}

func TestSearchTextScopedByFilePath(t *testing.T) {
	dir := t.TempDir()

	// same layout as the fallback test: board_a pair shares -IboardA,
	// board_b stands alone.
	cc := `[
  {"directory": "` + dir + `", "file": "` + filepath.Join(dir, "board_a/x.c") + `", "command": "gcc -Iinc -IboardA -c board_a/x.c"},
  {"directory": "` + dir + `", "file": "` + filepath.Join(dir, "board_a/x2.c") + `", "command": "gcc -Iinc -IboardA -c board_a/x2.c"},
  {"directory": "` + dir + `", "file": "` + filepath.Join(dir, "board_b/y.c") + `", "command": "gcc -IboardB -c board_b/y.c"}
]`
	if err := os.WriteFile(filepath.Join(dir, "compile_commands.json"), []byte(cc), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"board_a/x.c", "board_a/x2.c", "board_b/y.c"} {
		p := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("int secret_token;\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	r := NewRouter(dir)

	// anchored on a file with a discriminating neighborhood
	results, err := r.Search(context.Background(), SearchOptions{
		Query:    "secret_token",
		Strategy: "text",
		FilePath: filepath.Join(dir, "board_a", "x.c"),
	})
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	content := results[0].Content
	if !strings.Contains(content, "board_a/x.c") || !strings.Contains(content, "board_a/x2.c") {
		t.Errorf("expected hits in both board_a files, got:\n%s", content)
	}
	if strings.Contains(content, "board_b") {
		t.Errorf("out-of-neighborhood file leaked into text results:\n%s", content)
	}

	// anchored on a file without one: falls back to the file itself
	results, err = r.Search(context.Background(), SearchOptions{
		Query:    "secret_token",
		Strategy: "text",
		FilePath: filepath.Join(dir, "board_b", "y.c"),
	})
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	content = results[0].Content
	if !strings.Contains(content, "board_b/y.c") {
		t.Errorf("expected hit in the anchor file itself, got:\n%s", content)
	}
	if strings.Contains(content, "board_a") {
		t.Errorf("unanchored file leaked into single-file results:\n%s", content)
	}
}

func TestSnippetExpansion(t *testing.T) {
	dir := t.TempDir()
	content := "l1\nl2\nl3 TODO\nl4\nl5\nl6\nl7\n"
	if err := os.WriteFile(filepath.Join(dir, "a.c"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	exp := newSnippetExpander(dir)
	atoms := atomsFromTextMatches([]tools.TextMatch{
		{Path: "a.c", Line: 3, Offset: 6, Text: "l3 TODO"},
	}, "rg", exp)
	if len(atoms) != 1 {
		t.Fatalf("expected 1 atom, got %d", len(atoms))
	}

	a := atoms[0]
	if a.FullContent != "l1\nl2\nl3 TODO\nl4\nl5" {
		t.Errorf("unexpected expanded content:\n%s", a.FullContent)
	}
	if a.StartByte != 0 || a.EndByte != len("l1\nl2\nl3 TODO\nl4\nl5\n") {
		t.Errorf("unexpected byte range: [%d,%d)", a.StartByte, a.EndByte)
	}
	// signature stays the bare match line for L1 degradation
	if a.Signature != "l3 TODO" {
		t.Errorf("unexpected signature: %q", a.Signature)
	}

	// clamping at file start
	atoms = atomsFromTextMatches([]tools.TextMatch{
		{Path: "a.c", Line: 1, Offset: 0, Text: "l1"},
	}, "rg", exp)
	if got := atoms[0].FullContent; got != "l1\nl2\nl3 TODO" {
		t.Errorf("unexpected clamped content:\n%s", got)
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
