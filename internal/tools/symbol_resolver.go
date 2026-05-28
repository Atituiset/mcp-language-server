package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/isaacphi/mcp-language-server/internal/lsp"
	"github.com/isaacphi/mcp-language-server/internal/protocol"
)

// ResolveSymbolLocation resolves a human-readable symbol name to an LSP location.
// It is intentionally conservative: exact matches win, and filePath is treated
// as an optional disambiguation hint for chat-style tool calls.
func ResolveSymbolLocation(ctx context.Context, client *lsp.Client, symbolName, filePath string) (protocol.Location, error) {
	symbolResult, err := client.Symbol(ctx, protocol.WorkspaceSymbolParams{
		Query: symbolName,
	})
	if err != nil {
		return protocol.Location{}, fmt.Errorf("failed to fetch symbol: %w", err)
	}

	results, err := symbolResult.Results()
	if err != nil {
		return protocol.Location{}, fmt.Errorf("failed to parse symbol results: %w", err)
	}

	for _, symbol := range results {
		if !symbolNameMatches(symbol.GetName(), symbolName) {
			continue
		}

		loc := symbol.GetLocation()
		if filePath != "" && !locationMatchesFilePath(loc, filePath) {
			continue
		}

		return loc, nil
	}

	if filePath != "" {
		return protocol.Location{}, fmt.Errorf("symbol %q not found in %q", symbolName, filePath)
	}
	return protocol.Location{}, fmt.Errorf("symbol %q not found", symbolName)
}

func symbolNameMatches(candidate, query string) bool {
	if candidate == query {
		return true
	}

	if strings.Contains(query, ".") || strings.Contains(query, "::") {
		parts := strings.FieldsFunc(query, func(r rune) bool {
			return r == '.' || r == ':'
		})
		if len(parts) == 0 {
			return false
		}
		return candidate == parts[len(parts)-1]
	}

	return strings.HasSuffix(candidate, "::"+query) || strings.HasSuffix(candidate, "."+query)
}

func locationMatchesFilePath(loc protocol.Location, filePath string) bool {
	locationPath := loc.URI.Path()
	return locationPath == filePath || strings.HasSuffix(locationPath, "/"+strings.TrimPrefix(filePath, "/"))
}
