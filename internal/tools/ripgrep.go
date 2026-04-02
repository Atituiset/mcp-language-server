package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// RipgrepOptions contains options for ripgrep search
type RipgrepOptions struct {
	CaseSensitive bool
	WholeWord     bool
	MaxCount      int
	ContextLines  int
	FileType      string
	Include       string
}

// SearchCode performs a ripgrep search in the given workspace directory
func SearchCode(ctx context.Context, workspaceDir, pattern string, opts RipgrepOptions) (string, error) {
	args := []string{
		"--json",
		"--max-count", strconv.Itoa(opts.MaxCount),
	}

	if !opts.CaseSensitive {
		args = append(args, "--ignore-case")
	}
	if opts.WholeWord {
		args = append(args, "--word-regexp")
	}
	if opts.ContextLines > 0 {
		args = append(args, "-C", strconv.Itoa(opts.ContextLines))
	}
	if opts.FileType != "" {
		args = append(args, "-t", opts.FileType)
	}
	if opts.Include != "" {
		args = append(args, "--glob", opts.Include)
	}

	args = append(args, pattern, ".")

	cmd := exec.Command("rg", args...)
	cmd.Dir = workspaceDir

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	output, err := cmd.Output()
	if err != nil {
		if stderr.Len() > 0 {
			return "", fmt.Errorf("ripgrep error: %s", stderr.String())
		}
		return "", fmt.Errorf("ripgrep failed: %v", err)
	}

	return parseRipgrepOutput(output, opts.ContextLines)
}

type ripgrepMatch struct {
	Type     string `json:"type"`
	Path     struct {
		Text string `json:"text"`
	} `json:"path"`
	Lines struct {
		Text string `json:"text"`
	} `json:"lines"`
	LineNumber int `json:"line_number"`
	Submatches []struct {
		Start int `json:"start"`
		End   int `json:"end"`
	} `json:"submatches"`
}

func parseRipgrepOutput(output []byte, contextLines int) (string, error) {
	var result strings.Builder
	var currentFile string
	var fileMatchCount int

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}

		var match ripgrepMatch
		if err := json.Unmarshal([]byte(line), &match); err != nil {
			continue
		}

		if match.Type == "match" {
			if match.Path.Text != currentFile {
				if currentFile != "" {
					result.WriteString("\n")
				}
				result.WriteString(fmt.Sprintf("=== %s ===\n", match.Path.Text))
				currentFile = match.Path.Text
				fileMatchCount = 0
			}

			fileMatchCount++
			lineNum := match.LineNumber
			lineText := strings.TrimRight(match.Lines.Text, "\n")

			// Format with line number
			result.WriteString(fmt.Sprintf("%d: %s\n", lineNum, lineText))

			// Show context if requested
			if contextLines > 0 {
				// Context lines are included in the ripgrep -C output as separate "context" type entries
				// For simplicity, we just show the match line here
			}
		} else if match.Type == "context" || match.Type == "begin" || match.Type == "end" {
			// Context lines from -C flag
			lineNum := match.LineNumber
			lineText := strings.TrimRight(match.Lines.Text, "\n")
			result.WriteString(fmt.Sprintf("%d: %s\n", lineNum, lineText))
		}
	}

	if result.Len() == 0 {
		return "No matches found", nil
	}

	return result.String(), nil
}
