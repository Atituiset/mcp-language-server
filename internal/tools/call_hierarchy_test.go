package tools

import (
	"fmt"
	"strings"
	"testing"
)

func TestFormatCallResultsWithDepthTruncation(t *testing.T) {
	var results []CallResult
	for i := 0; i < 2000; i++ {
		results = append(results, CallResult{
			Name:     fmt.Sprintf("caller_function_%d", i),
			FilePath: "/some/long/path/to/source/file.c",
			Line:     i + 1,
			Column:   1,
			Depth:    (i % 3) + 1,
		})
	}

	out := formatCallResultsWithDepth(results, "Callers", 3)

	if len(out) > maxCallHierarchyBytes+256 { // header/marker slack
		t.Errorf("output exceeds cap: %d bytes", len(out))
	}
	if !strings.Contains(out, "... [truncated,") {
		t.Errorf("expected truncation marker, got tail:\n%s", out[len(out)-200:])
	}
	if !strings.Contains(out, "2000 total") {
		t.Errorf("header should report full result count, got:\n%s", out[:120])
	}
}

func TestFormatCallResultsWithDepthSmall(t *testing.T) {
	results := []CallResult{
		{Name: "foo", FilePath: "/a/b.c", Line: 10, Column: 2, Depth: 1},
		{Name: "bar", FilePath: "/a/c.c", Line: 20, Column: 3, Depth: 2},
	}
	out := formatCallResultsWithDepth(results, "Callers", 2)

	for _, want := range []string{"2 total", "foo at /a/b.c:L10:C2", "bar at /a/c.c:L20:C3"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q, got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "truncated") {
		t.Errorf("small result must not be truncated:\n%s", out)
	}
}
