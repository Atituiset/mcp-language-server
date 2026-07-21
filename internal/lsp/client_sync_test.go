package lsp

import (
	"testing"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/isaacphi/mcp-language-server/internal/protocol"
)

// offsetOfPosition converts an LSP position (line, UTF-16 character) back to
// a byte offset — the inverse of positionAt, used to apply a computed change
// to the old content and verify it reproduces the new content.
func offsetOfPosition(t *testing.T, content []byte, pos protocol.Position) int {
	t.Helper()
	offset := 0
	for line := uint32(0); line < pos.Line; line++ {
		idx := indexByte(content, offset, '\n')
		if idx < 0 {
			t.Fatalf("line %d out of range", pos.Line)
		}
		offset = idx + 1
	}
	var units uint32
	for offset < len(content) {
		r, size := utf8.DecodeRune(content[offset:])
		if r == '\n' || units >= pos.Character {
			break
		}
		if r > 0xFFFF {
			units += 2
		} else {
			units++
		}
		offset += size
	}
	if units != pos.Character {
		t.Fatalf("character %d out of range at offset %d", pos.Character, offset)
	}
	return offset
}

func indexByte(b []byte, from int, c byte) int {
	for i := from; i < len(b); i++ {
		if b[i] == c {
			return i
		}
	}
	return -1
}

// applyChanges round-trips a computed change set: it must transform old into
// new, and the replacement text must be valid UTF-8.
func applyChanges(t *testing.T, old, new []byte) {
	t.Helper()
	changes := computeIncrementalChanges(old, new)
	if len(changes) != 1 {
		t.Fatalf("expected exactly one change event, got %d", len(changes))
	}
	switch v := changes[0].Value.(type) {
	case protocol.TextDocumentContentChangeWholeDocument:
		if v.Text != string(new) {
			t.Errorf("whole-document text mismatch")
		}
	case protocol.TextDocumentContentChangePartial:
		if !utf8.ValidString(v.Text) {
			t.Fatalf("replacement text is not valid UTF-8: %q", v.Text)
		}
		start := offsetOfPosition(t, old, v.Range.Start)
		end := offsetOfPosition(t, old, v.Range.End)
		got := string(old[:start]) + v.Text + string(old[end:])
		if got != string(new) {
			t.Errorf("applied change does not reproduce new content\nold: %q\nnew: %q\ngot: %q", old, new, got)
		}
	default:
		t.Fatalf("unexpected change type %T", v)
	}
}

func TestComputeIncrementalChangesIdentical(t *testing.T) {
	if changes := computeIncrementalChanges([]byte("abc\ndef\n"), []byte("abc\ndef\n")); changes != nil {
		t.Errorf("expected nil for identical content, got %v", changes)
	}
}

func TestComputeIncrementalChanges(t *testing.T) {
	cases := []struct {
		name string
		old  string
		new  string
	}{
		{"single char", "int x = 1;\n", "int x = 2;\n"},
		{"insert line", "a\nc\n", "a\nb\nc\n"},
		{"delete line", "a\nb\nc\n", "a\nc\n"},
		{"append at end", "abc", "abcdef"},
		{"prepend at start", "abc", "xyzabc"},
		{"empty to content", "", "hello\n"},
		{"content to empty", "hello\n", ""},
		{"multibyte change", "héllo wörld\n", "héllo wäld\n"},
		{"rune replaced sharing lead byte", "aéb\n", "aèb\n"},
		{"emoji before change", "😀x = 1;\n", "😀x = 2;\n"},
		{"crlf", "a\r\nb\r\n", "a\r\nc\r\n"},
		{"chinese", "变量 := 1\n", "变量 := 2\n"},
		{"whole doc", "aaa\nbbb\n", "xxx\nyyy\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			applyChanges(t, []byte(tc.old), []byte(tc.new))
		})
	}
}

func TestComputeIncrementalChangesWholeDocument(t *testing.T) {
	// No shared prefix or suffix at all -> whole-document change.
	changes := computeIncrementalChanges([]byte("aaa"), []byte("xxx"))
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	if _, ok := changes[0].Value.(protocol.TextDocumentContentChangeWholeDocument); !ok {
		t.Errorf("expected whole-document change when prefix/suffix share nothing, got %T", changes[0].Value)
	}
}

func TestPositionAtUTF16(t *testing.T) {
	content := []byte("a😀b\ncd\n")
	cases := []struct {
		offset int
		line   uint32
		char   uint32
	}{
		{0, 0, 0},            // before 'a'
		{1, 0, 1},            // before emoji
		{5, 0, 3},            // after emoji (2 UTF-16 units), before 'b'
		{7, 1, 0},            // start of second line
		{len(content), 2, 0}, // after the trailing newline: start of a new empty line
	}
	for _, tc := range cases {
		got := positionAt(content, tc.offset)
		if got.Line != tc.line || got.Character != tc.char {
			t.Errorf("offset %d: expected %d:%d, got %d:%d", tc.offset, tc.line, tc.char, got.Line, got.Character)
		}
	}
}

// Sanity check that utf16 agrees with our surrogate-pair counting.
func TestPositionAtSurrogatePair(t *testing.T) {
	content := []byte("😀\n")
	got := positionAt(content, 4) // after the emoji, before '\n'
	if got.Character != uint32(len(utf16.Encode([]rune{'😀'}))) {
		t.Errorf("expected 2 UTF-16 units for emoji, got %d", got.Character)
	}
}
