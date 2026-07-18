package treesitter

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestQueryDirectoryParallel(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 8; i++ {
		src := fmt.Sprintf("int foo_%d(void) { return %d; }\n", i, i)
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%d.c", i)), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	results, err := QueryDirectory(dir, "(function_definition) @f", "c")
	if err != nil {
		t.Fatalf("QueryDirectory: %v", err)
	}
	if len(results) != 8 {
		t.Errorf("expected 8 function definitions, got %d", len(results))
	}

	files := map[string]bool{}
	for _, r := range results {
		files[r.FilePath] = true
		if r.EndByte <= r.StartByte {
			t.Errorf("invalid byte range in %+v", r)
		}
	}
	if len(files) != 8 {
		t.Errorf("expected results from all 8 files, got %d", len(files))
	}
}

func TestQueryDirectoryInvalidPatternFailsFast(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.c"), []byte("int x;\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := QueryDirectory(dir, "this is not a csp query", "c"); err == nil {
		t.Error("expected compile error for invalid pattern")
	}
}
