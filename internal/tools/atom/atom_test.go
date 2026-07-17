package atom

import (
	"strings"
	"testing"
)

func mkAtom(file string, start, end int, id string, prio float64) CodeAtom {
	return CodeAtom{
		SemanticID: id,
		Name:       id,
		Kind:       KindSnippet,
		FilePath:   file,
		StartByte:  start,
		EndByte:    end,
		Priority:   prio,
	}
}

func TestMergePhysical(t *testing.T) {
	atoms := []CodeAtom{
		mkAtom("a.c", 100, 500, "big", 1),  // container
		mkAtom("a.c", 200, 250, "frag", 1), // contained -> swallowed
		mkAtom("a.c", 300, 600, "over", 1), // overlaps big -> swallowed
		mkAtom("a.c", 700, 800, "free", 1), // disjoint -> kept
		mkAtom("b.c", 100, 200, "other", 1),
	}

	out := MergePhysical(atoms)
	ids := map[string]bool{}
	for _, a := range out {
		ids[a.SemanticID] = true
	}

	for _, want := range []string{"big", "free", "other"} {
		if !ids[want] {
			t.Errorf("expected atom %q to survive, got %v", want, ids)
		}
	}
	for _, gone := range []string{"frag", "over"} {
		if ids[gone] {
			t.Errorf("expected atom %q to be swallowed, got %v", gone, ids)
		}
	}
}

func TestMergePhysicalPrefersLargerRange(t *testing.T) {
	atoms := []CodeAtom{
		mkAtom("a.c", 100, 150, "small", 5), // same start, smaller range
		mkAtom("a.c", 100, 500, "big", 1),   // same start, larger range wins
	}
	out := MergePhysical(atoms)
	if len(out) != 1 || out[0].SemanticID != "big" {
		t.Errorf("expected larger-range atom to win, got %+v", out)
	}
}

func TestDedupSemantic(t *testing.T) {
	atoms := []CodeAtom{
		mkAtom("a.c", 0, 10, "dup", 1),
		mkAtom("b.c", 0, 10, "dup", 3), // higher priority wins
		mkAtom("c.c", 0, 10, "uniq", 2),
	}
	out := DedupSemantic(atoms)
	if len(out) != 2 {
		t.Fatalf("expected 2 atoms, got %d", len(out))
	}
	for _, a := range out {
		if a.SemanticID == "dup" && a.Priority != 3 {
			t.Errorf("expected highest-priority duplicate to survive, got %+v", a)
		}
	}
}

func TestCropBudgetDegradation(t *testing.T) {
	full := strings.Repeat("x", 100)
	sig := strings.Repeat("y", 30)
	ref := strings.Repeat("z", 10)

	atoms := []CodeAtom{
		{SemanticID: "a", Name: "a", FilePath: "f.c", FullContent: full, Signature: sig, Reference: ref, Priority: 3},
		{SemanticID: "b", Name: "b", FilePath: "f.c", FullContent: full, Signature: sig, Reference: ref, Priority: 2},
		{SemanticID: "c", Name: "c", FilePath: "f.c", FullContent: full, Signature: sig, Reference: ref, Priority: 1},
	}

	// Charged sizes incl. overhead: a@L0 = 100+37, b@L1 = 30+24, c@L2 = 10+24.
	kept, stats := CropBudget(atoms, 137+54+34)

	if len(kept) != 3 {
		t.Fatalf("expected all 3 atoms kept via degradation, got %d", len(kept))
	}
	levels := map[string]int{}
	for _, a := range kept {
		levels[a.SemanticID] = a.Level
	}
	if levels["a"] != LevelFull || levels["b"] != LevelSignature || levels["c"] != LevelReference {
		t.Errorf("unexpected degradation levels: %v", levels)
	}
	if stats.KeptFull != 1 || stats.KeptSignature != 1 || stats.KeptReference != 1 || stats.Dropped != 0 {
		t.Errorf("unexpected stats: %+v", stats)
	}
}

func TestCropBudgetDropsWhenReferenceTooBig(t *testing.T) {
	atoms := []CodeAtom{
		{SemanticID: "a", FilePath: "f.c", FullContent: strings.Repeat("x", 100), Priority: 2},
		{SemanticID: "b", FilePath: "f.c", Reference: strings.Repeat("z", 50), Priority: 1},
	}
	// a@L0 charged 100+37 = 137; b@L2 would charge 50+24 = 74 > remaining 0.
	kept, stats := CropBudget(atoms, 137)
	if len(kept) != 1 || kept[0].SemanticID != "a" {
		t.Fatalf("expected only atom a, got %+v", kept)
	}
	if stats.Dropped != 1 {
		t.Errorf("expected 1 dropped, got %+v", stats)
	}
}

func TestRender(t *testing.T) {
	atoms := []CodeAtom{
		{SemanticID: "a", Name: "foo", Kind: KindSnippet, FilePath: "f.c",
			Reference: "foo at f.c:10", SourceTool: "rg", Level: LevelReference},
	}
	stats := CropStats{Total: 2, KeptReference: 1, Dropped: 1, BytesUsed: 15, Budget: 1024}
	out := Render(atoms, stats)
	for _, want := range []string{"[unified]", "1 dropped", "=== f.c ===", "foo at f.c:10", "dropped by budget"} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q, got:\n%s", want, out)
		}
	}
}
