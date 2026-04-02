package tools

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/isaacphi/mcp-language-server/internal/lsp"
	"github.com/isaacphi/mcp-language-server/internal/protocol"
)

// GetCallers finds functions that call the function at the given position
func GetCallers(ctx context.Context, client *lsp.Client, filePath string, line, column int) (string, error) {
	// Open file if not already open
	if err := client.OpenFile(ctx, filePath); err != nil {
		return "", fmt.Errorf("failed to open file: %w", err)
	}

	// Prepare call hierarchy at position
	params := protocol.CallHierarchyPrepareParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: protocol.DocumentUri("file://" + filePath)},
			Position:     protocol.Position{Line: uint32(line - 1), Character: uint32(column - 1)},
		},
	}

	items, err := client.PrepareCallHierarchy(ctx, params)
	if err != nil {
		return "", fmt.Errorf("failed to prepare call hierarchy: %w", err)
	}

	if len(items) == 0 {
		return "No call hierarchy items found at this position", nil
	}

	// Get incoming calls (callers)
	var results []string
	for _, item := range items {
		incomingParams := protocol.CallHierarchyIncomingCallsParams{Item: item}
		incomingCalls, err := client.IncomingCalls(ctx, incomingParams)
		if err != nil {
			continue
		}

		for _, call := range incomingCalls {
			caller := call.From
			results = append(results, formatCallHierarchyItem(&caller, "caller"))
		}
	}

	if len(results) == 0 {
		return "No callers found", nil
	}

	return formatCallResults(results, "Callers"), nil
}

// GetCallees finds functions that are called by the function at the given position
func GetCallees(ctx context.Context, client *lsp.Client, filePath string, line, column int) (string, error) {
	// Open file if not already open
	if err := client.OpenFile(ctx, filePath); err != nil {
		return "", fmt.Errorf("failed to open file: %w", err)
	}

	// Prepare call hierarchy at position
	params := protocol.CallHierarchyPrepareParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: protocol.DocumentUri("file://" + filePath)},
			Position:     protocol.Position{Line: uint32(line - 1), Character: uint32(column - 1)},
		},
	}

	items, err := client.PrepareCallHierarchy(ctx, params)
	if err != nil {
		return "", fmt.Errorf("failed to prepare call hierarchy: %w", err)
	}

	if len(items) == 0 {
		return "No call hierarchy items found at this position", nil
	}

	// Get outgoing calls (callees)
	var results []string
	for _, item := range items {
		outgoingParams := protocol.CallHierarchyOutgoingCallsParams{Item: item}
		outgoingCalls, err := client.OutgoingCalls(ctx, outgoingParams)
		if err != nil {
			continue
		}

		for _, call := range outgoingCalls {
			callee := call.To
			results = append(results, formatCallHierarchyItem(&callee, "callee"))
		}
	}

	if len(results) == 0 {
		return "No callees found", nil
	}

	return formatCallResults(results, "Callees"), nil
}

func formatCallHierarchyItem(item *protocol.CallHierarchyItem, role string) string {
	name := item.Name
	if name == "" {
		name = "(anonymous)"
	}

	uri := strings.TrimPrefix(string(item.URI), "file://")
	rangeStr := formatRange(&item.Range)

	return fmt.Sprintf("%s: %s at %s:%s", role, name, uri, rangeStr)
}

func formatRange(r *protocol.Range) string {
	return fmt.Sprintf("L%d:C%d", r.Start.Line+1, r.Start.Character+1)
}

func formatCallResults(results []string, title string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("=== %s (%d) ===\n\n", title, len(results)))
	for _, r := range results {
		b.WriteString(r)
		b.WriteString("\n")
	}
	return b.String()
}

// ReadCallers reads file content for context
func readFileContext(path string, startLine, endLine int) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	lines := strings.Split(string(content), "\n")
	if startLine < 1 || startLine > len(lines) {
		return "", fmt.Errorf("invalid line range")
	}
	if endLine > len(lines) {
		endLine = len(lines)
	}
	return strings.Join(lines[startLine-1:endLine], "\n"), nil
}
