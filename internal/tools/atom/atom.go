// Package atom implements the CodeAtom IR from docs/code-atom-ir.md:
// a unified intermediate representation for heterogeneous search results
// (ripgrep / tree-sitter / LSP), with physical-range merging, semantic
// deduplication, and budget-based payload degradation.
//
// Phase 1 deviations from the design doc (documented there):
//   - No USR: LSP does not expose it; symbol atoms fall back to FQN-style IDs.
//   - Overlapping (non-contained) ranges are dropped, not LCA-merged.
//   - rg hits are not expanded to ±2 context lines.
//   - Symbol atoms carry no FullContent (no per-symbol definition fetch).
package atom

import (
	"fmt"
	"sort"
	"strings"
)

type Kind string

const (
	KindFunction Kind = "FUNCTION"
	KindStruct   Kind = "STRUCT"
	KindMacro    Kind = "MACRO"
	KindSnippet  Kind = "SNIPPET"
	KindSymbol   Kind = "SYMBOL"
)

// Payload levels for budget degradation (docs/code-atom-ir.md §2).
const (
	LevelFull      = iota // L0: complete content
	LevelSignature        // L1: signature only
	LevelReference        // L2: name + location only
)

type CodeAtom struct {
	// Semantic identity (cross-file dedup). USR > FQN > AST-hash per the
	// design doc; Phase 1 uses "name@path" for symbols, a path/type/hash
	// composite for AST nodes, and "path:byteOffset" for snippets.
	SemanticID string
	Name       string
	Kind       Kind

	// Spatial identity: byte offsets are the only source of truth.
	FilePath  string
	StartByte int
	EndByte   int

	// Multi-level payloads.
	FullContent string // L0
	Signature   string // L1
	Reference   string // L2

	SourceTool string  // "rg" | "tree-sitter" | "clangd"
	Priority   float64 // base: clangd=3, tree-sitter=2, rg=1

	// Level is the payload level chosen by CropBudget (LevelFull by default).
	Level int
}

// payloadLen returns the rendered size of the atom at the given level.
func (a *CodeAtom) payloadLen(level int) int {
	switch level {
	case LevelFull:
		return len(a.FullContent)
	case LevelSignature:
		return len(a.Signature)
	default:
		return len(a.Reference)
	}
}

// payload returns the payload text at the atom's chosen Level.
func (a *CodeAtom) payload() string {
	switch a.Level {
	case LevelFull:
		return a.FullContent
	case LevelSignature:
		return a.Signature
	default:
		return a.Reference
	}
}

// MergePhysical performs the sweep-line merge (docs §3.1): within each file,
// atoms sorted by StartByte; an atom whose range is contained in or overlaps
// an already-kept atom's range is swallowed. The kept atom is the one with
// the earlier start; ties break toward the larger range, then higher Priority.
func MergePhysical(atoms []CodeAtom) []CodeAtom {
	byFile := make(map[string][]int)
	for i := range atoms {
		byFile[atoms[i].FilePath] = append(byFile[atoms[i].FilePath], i)
	}

	dropped := make(map[int]bool)
	for _, idxs := range byFile {
		sort.Slice(idxs, func(a, b int) bool {
			x, y := atoms[idxs[a]], atoms[idxs[b]]
			if x.StartByte != y.StartByte {
				return x.StartByte < y.StartByte
			}
			if xr, yr := x.EndByte-x.StartByte, y.EndByte-y.StartByte; xr != yr {
				return xr > yr
			}
			return x.Priority > y.Priority
		})

		maxEnd := -1
		for _, i := range idxs {
			if atoms[i].StartByte < 0 {
				continue // unknown coordinates: bypass physical merging
			}
			if atoms[i].EndByte <= maxEnd || (atoms[i].StartByte < maxEnd && maxEnd >= 0) {
				// contained in, or overlapping, a kept atom's range
				dropped[i] = true
				continue
			}
			if atoms[i].EndByte > maxEnd {
				maxEnd = atoms[i].EndByte
			}
		}
	}

	out := make([]CodeAtom, 0, len(atoms)-len(dropped))
	for i := range atoms {
		if !dropped[i] {
			out = append(out, atoms[i])
		}
	}
	return out
}

// DedupSemantic collapses atoms sharing a SemanticID (docs §3.2), keeping
// the highest-Priority instance (hot-symbol weight promotion).
func DedupSemantic(atoms []CodeAtom) []CodeAtom {
	best := make(map[string]int) // SemanticID -> index in out
	var out []CodeAtom
	for _, a := range atoms {
		i, seen := best[a.SemanticID]
		if !seen {
			best[a.SemanticID] = len(out)
			out = append(out, a)
			continue
		}
		if a.Priority > out[i].Priority {
			out[i] = a
		}
	}
	return out
}

// CropStats reports the four degradation phases (docs §4).
type CropStats struct {
	Total         int
	KeptFull      int
	KeptSignature int
	KeptReference int
	Dropped       int
	BytesUsed     int
	Budget        int
}

// renderOverhead is the per-atom rendered cost beyond the payload itself
// (tag + newline), plus a per-file header charged on first occurrence.
const (
	renderAtomOverhead = 24
	renderFileOverhead = 10
)

// CropBudget greedily fits atoms into the byte budget (docs §4): atoms are
// visited by descending Priority; each is tried at L0, then L1, then L2, and
// dropped only when even its Reference does not fit. Charged sizes include
// the rendered overhead (tags, file headers), so the budget is honest.
func CropBudget(atoms []CodeAtom, budget int) ([]CodeAtom, CropStats) {
	sort.SliceStable(atoms, func(i, j int) bool {
		if atoms[i].Priority != atoms[j].Priority {
			return atoms[i].Priority > atoms[j].Priority
		}
		if atoms[i].FilePath != atoms[j].FilePath {
			return atoms[i].FilePath < atoms[j].FilePath
		}
		return atoms[i].StartByte < atoms[j].StartByte
	})

	stats := CropStats{Total: len(atoms), Budget: budget}
	kept := make([]CodeAtom, 0, len(atoms))
	seenFiles := make(map[string]bool)
	for _, a := range atoms {
		overhead := renderAtomOverhead
		if !seenFiles[a.FilePath] {
			overhead += len(a.FilePath) + renderFileOverhead
		}
		placed := false
		for level := LevelFull; level <= LevelReference; level++ {
			size := a.payloadLen(level)
			if size == 0 {
				continue // empty payload at this level, try the next one down
			}
			if stats.BytesUsed+size+overhead <= budget {
				a.Level = level
				stats.BytesUsed += size + overhead
				seenFiles[a.FilePath] = true
				switch level {
				case LevelFull:
					stats.KeptFull++
				case LevelSignature:
					stats.KeptSignature++
				default:
					stats.KeptReference++
				}
				kept = append(kept, a)
				placed = true
				break
			}
		}
		if !placed {
			stats.Dropped++
		}
	}
	return kept, stats
}

// Render formats the surviving atoms grouped by file, with a statistics
// header and a narrowing hint when the budget dropped atoms.
func Render(atoms []CodeAtom, stats CropStats) string {
	return RenderWithLabel(atoms, stats, "unified")
}

// RenderWithLabel is Render with a custom layer label in the header.
func RenderWithLabel(atoms []CodeAtom, stats CropStats, label string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "=== [%s] (%d atoms, %d shown: %d full / %d signature / %d reference, %d dropped, %.1fKB of %.0fKB budget) ===\n\n",
		label, stats.Total, stats.Total-stats.Dropped,
		stats.KeptFull, stats.KeptSignature, stats.KeptReference, stats.Dropped,
		float64(stats.BytesUsed)/1024, float64(stats.Budget)/1024)

	byFile := make(map[string][]CodeAtom)
	var files []string
	for _, a := range atoms {
		if _, seen := byFile[a.FilePath]; !seen {
			files = append(files, a.FilePath)
		}
		byFile[a.FilePath] = append(byFile[a.FilePath], a)
	}
	sort.Strings(files)

	for _, f := range files {
		group := byFile[f]
		sort.Slice(group, func(i, j int) bool { return group[i].StartByte < group[j].StartByte })
		fmt.Fprintf(&b, "=== %s ===\n", f)
		for _, a := range group {
			payload := strings.TrimRight(a.payload(), "\n")
			if payload == "" {
				continue
			}
			fmt.Fprintf(&b, "[%s|%s|%s] %s\n", a.SourceTool, a.Kind, levelTag(a.Level), payload)
		}
		b.WriteString("\n")
	}

	if stats.Dropped > 0 {
		fmt.Fprintf(&b, "... [%d atoms dropped by budget, use strategy=text|ast|symbol with filePath to narrow]\n", stats.Dropped)
	}
	return b.String()
}

func levelTag(level int) string {
	switch level {
	case LevelFull:
		return "L0"
	case LevelSignature:
		return "L1"
	default:
		return "L2"
	}
}
