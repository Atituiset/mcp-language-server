package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func writeCompileCommands(t *testing.T, dir string) {
	t.Helper()
	cc := `[
  {"directory": "` + dir + `", "file": "` + filepath.Join(dir, "board_a/main.c") + `", "command": "gcc -Iinclude -Iboard/a/include -isystem /usr/lib/gcc/include -c board_a/main.c"},
  {"directory": "` + dir + `", "file": "` + filepath.Join(dir, "board_a/driver.c") + `", "command": "gcc -Iinclude -Iboard/a/include -c board_a/driver.c"},
  {"directory": "` + dir + `", "file": "` + filepath.Join(dir, "board_b/main.c") + `", "command": "gcc", "arguments": ["gcc", "-I", "include", "-Iboard/b/include", "-c", "board_b/main.c"]}
]`
	if err := os.WriteFile(filepath.Join(dir, "compile_commands.json"), []byte(cc), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadIncludeMapAndNeighborhood(t *testing.T) {
	dir := t.TempDir()
	writeCompileCommands(t, dir)

	m, err := LoadIncludeMap(dir)
	if err != nil {
		t.Fatalf("LoadIncludeMap: %v", err)
	}
	if m.Size() != 3 {
		t.Fatalf("expected 3 TUs, got %d", m.Size())
	}

	// board_a/main.c and board_a/driver.c share both include dirs -> neighborhood of 2
	nb := m.Neighborhood(filepath.Join(dir, "board_a", "main.c"))
	if len(nb) != 2 {
		t.Fatalf("expected neighborhood of 2, got %d: %v", len(nb), nb)
	}
	for _, f := range nb {
		if filepath.Dir(f) != filepath.Join(dir, "board_a") {
			t.Errorf("unexpected neighbor: %s", f)
		}
	}

	// unknown file -> nil
	if nb := m.Neighborhood(filepath.Join(dir, "ghost.c")); nb != nil {
		t.Errorf("expected nil neighborhood for unknown file, got %v", nb)
	}
}

func TestNeighborhoodNoDiscrimination(t *testing.T) {
	dir := t.TempDir()
	// every TU shares the same single include dir -> neighborhood == whole map -> nil
	cc := `[
  {"directory": "` + dir + `", "file": "` + filepath.Join(dir, "a.c") + `", "command": "gcc -Iinclude -c a.c"},
  {"directory": "` + dir + `", "file": "` + filepath.Join(dir, "b.c") + `", "command": "gcc -Iinclude -c b.c"}
]`
	if err := os.WriteFile(filepath.Join(dir, "compile_commands.json"), []byte(cc), 0o644); err != nil {
		t.Fatal(err)
	}

	m, err := LoadIncludeMap(dir)
	if err != nil {
		t.Fatalf("LoadIncludeMap: %v", err)
	}
	if nb := m.Neighborhood(filepath.Join(dir, "a.c")); nb != nil {
		t.Errorf("expected nil (no discrimination), got %v", nb)
	}
}

func TestLoadIncludeMapMissing(t *testing.T) {
	if _, err := LoadIncludeMap(t.TempDir()); err == nil {
		t.Error("expected error for missing compile_commands.json")
	}
}
