package router

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/isaacphi/mcp-language-server/internal/protocol"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func containsAll(s string, subs []string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}

// stubSymbolClient implements the router's symbolClient interface for tests.
type stubSymbolClient struct {
	symbols []protocol.SymbolInformation
	alive   bool
}

func (s *stubSymbolClient) Symbol(_ context.Context, _ protocol.WorkspaceSymbolParams) (protocol.Or_Result_workspace_symbol, error) {
	return protocol.Or_Result_workspace_symbol{Value: s.symbols}, nil
}

func (s *stubSymbolClient) OpenFile(_ context.Context, _ string) error { return nil }

func (s *stubSymbolClient) DocumentSymbol(_ context.Context, _ protocol.DocumentSymbolParams) (protocol.Or_Result_textDocument_documentSymbol, error) {
	return protocol.Or_Result_textDocument_documentSymbol{}, nil
}

func (s *stubSymbolClient) Alive() bool { return s.alive }

// A live LSP client with matching symbols must produce a real symbol-layer
// result without any fallback warning.
func TestSearchSymbolWithLiveClient(t *testing.T) {
	dir := t.TempDir()
	stub := &stubSymbolClient{
		alive: true,
		symbols: []protocol.SymbolInformation{
			{
				Name: "my_function",
				Location: protocol.Location{
					URI:   protocol.DocumentUri("file://" + dir + "/main.c"),
					Range: protocol.Range{Start: protocol.Position{Line: 2}},
				},
			},
		},
	}

	r := NewRouterWithClient(dir, stub)
	results, err := r.Search(context.Background(), SearchOptions{Query: "my_function", Strategy: "symbol"})
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(results) != 1 || results[0].Layer != "symbol" {
		t.Fatalf("expected symbol layer, got %+v", results)
	}
	if got := results[0].Content; !containsAll(got, []string{"my_function", "main.c"}) {
		t.Errorf("unexpected symbol content:\n%s", got)
	}
}

// A dead LSP client must fall back to text search with a warning that names
// the real reason (process exited), not the generic "unavailable".
func TestSearchFallbackWarningNamesDeadProcess(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir+"/main.c", "int secret_token;\n")

	stub := &stubSymbolClient{alive: false}
	r := NewRouterWithClient(dir, stub)
	results, err := r.Search(context.Background(), SearchOptions{Query: "secret_token", Strategy: "symbol"})
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(results) != 1 || results[0].Layer != "symbol-fallback-text" {
		t.Fatalf("expected symbol-fallback-text layer, got %+v", results)
	}
	want := "WARNING: LSP server process exited"
	if !containsAll(results[0].Content, []string{want}) {
		t.Errorf("expected %q in fallback output, got:\n%s", want, results[0].Content)
	}
}
