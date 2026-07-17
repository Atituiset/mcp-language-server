package tools

import (
	"strings"
	"testing"
)

func TestParseRipgrepOutput(t *testing.T) {
	// Real ripgrep --json output nests fields under "data".
	sample := `{"type":"begin","data":{"path":{"text":"common/stdio.c"}}}
{"type":"match","data":{"path":{"text":"common/stdio.c"},"lines":{"text":" * TODO: fix this\n"},"line_number":145,"absolute_offset":3264,"submatches":[{"match":{"text":"TODO"},"start":3,"end":7}]}}
{"type":"match","data":{"path":{"text":"common/stdio.c"},"lines":{"text":" * TODO: and that\n"},"line_number":200,"absolute_offset":5000,"submatches":[{"match":{"text":"TODO"},"start":3,"end":7}]}}
{"type":"end","data":{"path":{"text":"common/stdio.c"},"binary_offset":null,"stats":{}}}
{"type":"match","data":{"path":{"text":"net/net.c"},"lines":{"text":"// TODO: rework\n"},"line_number":42,"absolute_offset":1000,"submatches":[{"match":{"text":"TODO"},"start":3,"end":7}]}}`

	out, err := parseRipgrepOutput([]byte(sample), 0)
	if err != nil {
		t.Fatalf("parseRipgrepOutput error: %v", err)
	}

	expects := []string{
		"=== common/stdio.c ===",
		"145:  * TODO: fix this",
		"200:  * TODO: and that",
		"=== net/net.c ===",
		"42: // TODO: rework",
	}
	for _, e := range expects {
		if !strings.Contains(out, e) {
			t.Errorf("output missing %q, got:\n%s", e, out)
		}
	}

	// begin/end entries must not produce bogus "0: " lines
	for _, ln := range strings.Split(out, "\n") {
		if strings.HasPrefix(ln, "0: ") {
			t.Errorf("output contains bogus zero line-number entry %q in:\n%s", ln, out)
		}
	}
}

func TestParseRipgrepOutputNoMatches(t *testing.T) {
	out, err := parseRipgrepOutput([]byte(""), 0)
	if err != nil {
		t.Fatalf("parseRipgrepOutput error: %v", err)
	}
	if out != "No matches found" {
		t.Errorf("expected 'No matches found', got %q", out)
	}
}
