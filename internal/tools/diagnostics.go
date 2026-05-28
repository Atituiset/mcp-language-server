package tools

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/isaacphi/mcp-language-server/internal/lsp"
	"github.com/isaacphi/mcp-language-server/internal/protocol"
)

type DiagnosticItem struct {
	Severity string `json:"severity"`
	Line     int    `json:"line"`
	Column   int    `json:"column"`
	Message  string `json:"message"`
	Source   string `json:"source,omitempty"`
	Code     any    `json:"code,omitempty"`
	FilePath string `json:"filePath"`
}

type DiagnosticsData struct {
	FilePath     string           `json:"filePath"`
	Total        int              `json:"total"`
	ErrorCount   int              `json:"errorCount"`
	WarningCount int              `json:"warningCount"`
	InfoCount    int              `json:"infoCount"`
	HintCount    int              `json:"hintCount"`
	Items        []DiagnosticItem `json:"items"`
	ContextLines []SourceLine     `json:"contextLines,omitempty"`
}

type SourceLine struct {
	Line    int    `json:"line"`
	Content string `json:"content"`
}

// GetDiagnosticsForFile retrieves diagnostics for a specific file from the language server
func GetDiagnosticsForFile(ctx context.Context, client *lsp.Client, filePath string, contextLines int, showLineNumbers bool) (string, error) {
	data, err := GetDiagnosticsDataForFile(ctx, client, filePath, contextLines, showLineNumbers)
	if err != nil {
		return "", err
	}
	return FormatDiagnosticsData(data, showLineNumbers), nil
}

func FormatDiagnosticsData(data DiagnosticsData, showLineNumbers bool) string {
	if data.Total == 0 {
		return "No diagnostics found for " + data.FilePath
	}

	var diagSummaries []string
	for _, diag := range data.Items {
		location := fmt.Sprintf("L%d:C%d", diag.Line, diag.Column)
		summary := fmt.Sprintf("%s at %s: %s", diag.Severity, location, diag.Message)
		if diag.Source != "" {
			summary += fmt.Sprintf(" (Source: %s", diag.Source)
			if diag.Code != nil {
				summary += fmt.Sprintf(", Code: %v", diag.Code)
			}
			summary += ")"
		} else if diag.Code != nil {
			summary += fmt.Sprintf(" (Code: %v)", diag.Code)
		}
		diagSummaries = append(diagSummaries, summary)
	}

	result := fmt.Sprintf("%s\nDiagnostics in File: %d\n", data.FilePath, data.Total)
	if len(diagSummaries) > 0 {
		result += strings.Join(diagSummaries, "\n") + "\n"
	}
	if showLineNumbers && len(data.ContextLines) > 0 {
		var lines []string
		for _, line := range data.ContextLines {
			lines = append(lines, fmt.Sprintf("%4d | %s", line.Line, line.Content))
		}
		result += "\n" + strings.Join(lines, "\n")
	}

	return result
}

func GetDiagnosticsDataForFile(ctx context.Context, client *lsp.Client, filePath string, contextLines int, includeContext bool) (DiagnosticsData, error) {
	data := DiagnosticsData{FilePath: filePath}
	// Override with environment variable if specified
	if envLines := os.Getenv("LSP_CONTEXT_LINES"); envLines != "" {
		if val, err := strconv.Atoi(envLines); err == nil && val >= 0 {
			contextLines = val
		}
	}

	err := client.OpenFile(ctx, filePath)
	if err != nil {
		return data, fmt.Errorf("could not open file: %v", err)
	}

	uri := protocol.URIFromPath(filePath)

	received := client.WaitForDiagnostics(uri, 5*time.Second)
	if !received {
		toolsLogger.Warn("Diagnostics wait timed out for %s, falling back to cache", filePath)
	}

	_, err = client.Diagnostic(ctx, protocol.DocumentDiagnosticParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: uri},
	})
	if err != nil {
		return data, fmt.Errorf("failed to request diagnostics: %v", err)
	}
	client.WaitForDiagnostics(uri, 3*time.Second)

	// Get diagnostics from the cache
	diagnostics := client.GetFileDiagnostics(uri)

	if len(diagnostics) == 0 {
		return data, nil
	}

	// Create a summary of all the diagnostics
	var diagLocations []protocol.Location

	for _, diag := range diagnostics {
		severity := getSeverityString(diag.Severity)
		switch severity {
		case "ERROR":
			data.ErrorCount++
		case "WARNING":
			data.WarningCount++
		case "INFO":
			data.InfoCount++
		case "HINT":
			data.HintCount++
		}

		data.Items = append(data.Items, DiagnosticItem{
			Severity: severity,
			Line:     int(diag.Range.Start.Line + 1),
			Column:   int(diag.Range.Start.Character + 1),
			Message:  diag.Message,
			Source:   diag.Source,
			Code:     diag.Code,
			FilePath: filePath,
		})

		// Create a location for this diagnostic to use with line ranges
		diagLocations = append(diagLocations, protocol.Location{
			URI:   uri,
			Range: diag.Range,
		})
	}
	data.Total = len(data.Items)

	// Format content with context
	fileContent, err := os.ReadFile(filePath)
	if err != nil {
		return data, nil
	}

	lines := strings.Split(string(fileContent), "\n")

	// Collect lines to display
	var linesToShow map[int]bool
	if contextLines > 0 {
		// Use GetLineRangesToDisplay for context
		linesToShow, err = GetLineRangesToDisplay(ctx, client, diagLocations, len(lines), contextLines)
		if err != nil {
			// If error, just show the diagnostic lines
			linesToShow = make(map[int]bool)
			for _, diag := range diagnostics {
				linesToShow[int(diag.Range.Start.Line)] = true
			}
		}
	} else {
		// Just show the diagnostic lines
		linesToShow = make(map[int]bool)
		for _, diag := range diagnostics {
			linesToShow[int(diag.Range.Start.Line)] = true
		}
	}

	if includeContext {
		var lineNumbers []int
		for line := range linesToShow {
			lineNumbers = append(lineNumbers, line)
		}
		sort.Ints(lineNumbers)
		for _, lineNumber := range lineNumbers {
			if lineNumber >= 0 && lineNumber < len(lines) {
				data.ContextLines = append(data.ContextLines, SourceLine{
					Line:    lineNumber + 1,
					Content: lines[lineNumber],
				})
			}
		}
	}

	return data, nil
}

func getSeverityString(severity protocol.DiagnosticSeverity) string {
	switch severity {
	case protocol.SeverityError:
		return "ERROR"
	case protocol.SeverityWarning:
		return "WARNING"
	case protocol.SeverityInformation:
		return "INFO"
	case protocol.SeverityHint:
		return "HINT"
	default:
		return "UNKNOWN"
	}
}
