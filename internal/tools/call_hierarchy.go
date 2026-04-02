package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/isaacphi/mcp-language-server/internal/lsp"
	"github.com/isaacphi/mcp-language-server/internal/protocol"
)

type CallResult struct {
	Name     string
	FilePath string
	Line     int
	Column   int
	Depth    int
}

// GetCallers finds functions that call the function at the given position, up to specified depth
func GetCallers(ctx context.Context, client *lsp.Client, filePath string, line, column, depth int) (string, error) {
	if depth <= 0 {
		depth = 1
	}

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

	// Collect all items at initial position
	var startItems []*protocol.CallHierarchyItem
	for i := range items {
		startItems = append(startItems, &items[i])
	}

	// Recursively find callers up to depth
	seen := make(map[string]bool) // Track seen items to avoid cycles
	var results []CallResult

	for _, item := range startItems {
		key := itemKey(item)
		seen[key] = true
	}

	collectCallers(ctx, client, startItems, depth, 1, seen, &results)

	if len(results) == 0 {
		return "No callers found", nil
	}

	return formatCallResultsWithDepth(results, "Callers", depth), nil
}

func collectCallers(ctx context.Context, client *lsp.Client, items []*protocol.CallHierarchyItem, maxDepth, currentDepth int, seen map[string]bool, results *[]CallResult) {
	if currentDepth > maxDepth || len(items) == 0 {
		return
	}

	for _, item := range items {
		incomingParams := protocol.CallHierarchyIncomingCallsParams{Item: *item}
		incomingCalls, err := client.IncomingCalls(ctx, incomingParams)
		if err != nil {
			continue
		}

		for _, call := range incomingCalls {
			caller := call.From
			key := itemKey(&caller)
			if seen[key] {
				continue
			}
			seen[key] = true

			*results = append(*results, CallResult{
				Name:     caller.Name,
				FilePath: trimFileURI(string(caller.URI)),
				Line:     int(caller.Range.Start.Line + 1),
				Column:   int(caller.Range.Start.Character + 1),
				Depth:    currentDepth,
			})

			// Recurse if not at max depth
			if currentDepth < maxDepth {
				collectCallers(ctx, client, []*protocol.CallHierarchyItem{&caller}, maxDepth, currentDepth+1, seen, results)
			}
		}
	}
}

// GetCallees finds functions that are called by the function at the given position, up to specified depth
func GetCallees(ctx context.Context, client *lsp.Client, filePath string, line, column, depth int) (string, error) {
	if depth <= 0 {
		depth = 1
	}

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

	// Collect all items at initial position
	var startItems []*protocol.CallHierarchyItem
	for i := range items {
		startItems = append(startItems, &items[i])
	}

	// Recursively find callees up to depth
	seen := make(map[string]bool) // Track seen items to avoid cycles
	var results []CallResult

	for _, item := range startItems {
		key := itemKey(item)
		seen[key] = true
	}

	collectCallees(ctx, client, startItems, depth, 1, seen, &results)

	if len(results) == 0 {
		return "No callees found", nil
	}

	return formatCallResultsWithDepth(results, "Callees", depth), nil
}

func collectCallees(ctx context.Context, client *lsp.Client, items []*protocol.CallHierarchyItem, maxDepth, currentDepth int, seen map[string]bool, results *[]CallResult) {
	if currentDepth > maxDepth || len(items) == 0 {
		return
	}

	for _, item := range items {
		outgoingParams := protocol.CallHierarchyOutgoingCallsParams{Item: *item}
		outgoingCalls, err := client.OutgoingCalls(ctx, outgoingParams)
		if err != nil {
			continue
		}

		for _, call := range outgoingCalls {
			callee := call.To
			key := itemKey(&callee)
			if seen[key] {
				continue
			}
			seen[key] = true

			*results = append(*results, CallResult{
				Name:     callee.Name,
				FilePath: trimFileURI(string(callee.URI)),
				Line:     int(callee.Range.Start.Line + 1),
				Column:   int(callee.Range.Start.Character + 1),
				Depth:    currentDepth,
			})

			// Recurse if not at max depth
			if currentDepth < maxDepth {
				collectCallees(ctx, client, []*protocol.CallHierarchyItem{&callee}, maxDepth, currentDepth+1, seen, results)
			}
		}
	}
}

func itemKey(item *protocol.CallHierarchyItem) string {
	return fmt.Sprintf("%s:%d:%d", string(item.URI), item.Range.Start.Line, item.Range.Start.Character)
}

func trimFileURI(uri string) string {
	return strings.TrimPrefix(uri, "file://")
}

func formatCallResultsWithDepth(results []CallResult, title string, maxDepth int) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("=== %s (depth 1-%d, %d total) ===\n\n", title, maxDepth, len(results)))

	// Group by depth
	depthGroups := make(map[int][]CallResult)
	for _, r := range results {
		depthGroups[r.Depth] = append(depthGroups[r.Depth], r)
	}

	for d := 1; d <= maxDepth; d++ {
		if group, ok := depthGroups[d]; ok {
			b.WriteString(fmt.Sprintf("--- Depth %d (%d functions) ---\n", d, len(group)))
			for _, r := range group {
				b.WriteString(fmt.Sprintf("  %s at %s:L%d:C%d\n", r.Name, r.FilePath, r.Line, r.Column))
			}
			b.WriteString("\n")
		}
	}

	return b.String()
}
