package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// IncludeMap maps each translation unit in compile_commands.json to the
// include directories it was compiled with, and each include directory back
// to the translation units using it. It is used to scope fallback searches
// (rg / tree-sitter) to a file's compile neighborhood instead of the whole
// workspace — the common case in multi-board, macro-gated C/C++ codebases
// where clangd only indexes the active defconfig's branches.
type IncludeMap struct {
	byFile map[string]map[string]bool // source file -> include dirs
	byDir  map[string]map[string]bool // include dir -> source files
}

// LoadIncludeMap parses <workspaceDir>/compile_commands.json.
func LoadIncludeMap(workspaceDir string) (*IncludeMap, error) {
	data, err := os.ReadFile(filepath.Join(workspaceDir, "compile_commands.json"))
	if err != nil {
		return nil, err
	}

	var entries []struct {
		File      string   `json:"file"`
		Directory string   `json:"directory"`
		Command   string   `json:"command"`
		Arguments []string `json:"arguments"`
	}
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, err
	}

	m := &IncludeMap{
		byFile: make(map[string]map[string]bool),
		byDir:  make(map[string]map[string]bool),
	}
	for _, e := range entries {
		file := e.File
		if !filepath.IsAbs(file) {
			file = filepath.Join(e.Directory, file)
		}
		file = filepath.Clean(file)

		args := e.Arguments
		if len(args) == 0 {
			args = strings.Fields(e.Command)
		}
		for _, dir := range extractIncludeDirs(args, e.Directory) {
			if m.byFile[file] == nil {
				m.byFile[file] = make(map[string]bool)
			}
			m.byFile[file][dir] = true
			if m.byDir[dir] == nil {
				m.byDir[dir] = make(map[string]bool)
			}
			m.byDir[dir][file] = true
		}
	}
	return m, nil
}

// extractIncludeDirs pulls -I/-isystem/-iquote paths out of a compile
// command line, resolving relative paths against the entry's directory.
func extractIncludeDirs(args []string, base string) []string {
	var dirs []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		var p string
		switch {
		case a == "-I" || a == "-isystem" || a == "-iquote":
			if i+1 < len(args) {
				i++
				p = args[i]
			}
		case strings.HasPrefix(a, "-I"):
			p = a[2:]
		case strings.HasPrefix(a, "-isystem"):
			p = a[len("-isystem"):]
		case strings.HasPrefix(a, "-iquote"):
			p = a[len("-iquote"):]
		}
		if p == "" {
			continue
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(base, p)
		}
		dirs = append(dirs, filepath.Clean(p))
	}
	return dirs
}

// Size returns the number of translation units in the map.
func (m *IncludeMap) Size() int {
	return len(m.byFile)
}

// Neighborhood returns the sorted set of files sharing at least one
// *discriminating* include directory with the given file — a directory is
// discriminating when it is not used by every translation unit in the map
// (global dirs like u-boot's include/ would otherwise make the whole
// workspace a single neighborhood). Returns nil when the file is unknown or
// no discriminating directory exists.
func (m *IncludeMap) Neighborhood(file string) []string {
	file = filepath.Clean(file)
	dirs := m.byFile[file]
	if len(dirs) == 0 {
		return nil
	}

	set := map[string]bool{}
	for d := range dirs {
		if len(m.byDir[d]) >= m.Size() {
			continue // global include dir: no discrimination
		}
		for f := range m.byDir[d] {
			set[f] = true
		}
	}
	if len(set) <= 1 {
		return nil
	}

	out := make([]string, 0, len(set))
	for f := range set {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}
