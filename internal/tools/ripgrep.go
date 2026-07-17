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
	output, err := runRipgrep(ctx, workspaceDir, pattern, opts)
	if err != nil {
		return "", err
	}
	return parseRipgrepOutput(output, opts.ContextLines)
}

// TextMatch is a single ripgrep match with its position and byte offset.
type TextMatch struct {
	Path   string // path relative to the workspace
	Line   int    // 1-indexed line number
	Offset int    // byte offset of the matched line start (rg absolute_offset)
	Text   string // trimmed line content
}

// SearchCodeMatches is the structured variant of SearchCode: it returns
// parsed matches instead of formatted text, for normalization into atoms.
func SearchCodeMatches(ctx context.Context, workspaceDir, pattern string, opts RipgrepOptions) ([]TextMatch, error) {
	output, err := runRipgrep(ctx, workspaceDir, pattern, opts)
	if err != nil {
		return nil, err
	}
	return parseRipgrepMatches(output), nil
}

func runRipgrep(ctx context.Context, workspaceDir, pattern string, opts RipgrepOptions) ([]byte, error) {
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
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 && stderr.Len() == 0 {
			return output, nil // rg exit 1: no matches, not an error
		}
		if stderr.Len() > 0 {
			return nil, fmt.Errorf("ripgrep error: %s", stderr.String())
		}
		return nil, fmt.Errorf("ripgrep failed: %v", err)
	}

	return output, nil
}

type ripgrepMatch struct {
	Type string `json:"type"`
	Data struct {
		Path struct {
			Text string `json:"text"`
		} `json:"path"`
		Lines struct {
			Text string `json:"text"`
		} `json:"lines"`
		LineNumber     int `json:"line_number"`
		AbsoluteOffset int `json:"absolute_offset"`
		Submatches     []struct {
			Start int `json:"start"`
			End   int `json:"end"`
		} `json:"submatches"`
	} `json:"data"`
}

// parseRipgrepMatches converts ripgrep --json output into structured matches.
func parseRipgrepMatches(output []byte) []TextMatch {
	var matches []TextMatch

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}

		var match ripgrepMatch
		if err := json.Unmarshal([]byte(line), &match); err != nil {
			continue
		}
		if match.Type != "match" {
			continue
		}

		matches = append(matches, TextMatch{
			Path:   match.Data.Path.Text,
			Line:   match.Data.LineNumber,
			Offset: match.Data.AbsoluteOffset,
			Text:   strings.TrimRight(match.Data.Lines.Text, "\n"),
		})
	}

	return matches
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
			if match.Data.Path.Text != currentFile {
				if currentFile != "" {
					result.WriteString("\n")
				}
				result.WriteString(fmt.Sprintf("=== %s ===\n", match.Data.Path.Text))
				currentFile = match.Data.Path.Text
				fileMatchCount = 0
			}

			fileMatchCount++
			lineNum := match.Data.LineNumber
			lineText := strings.TrimRight(match.Data.Lines.Text, "\n")

			// Format with line number
			result.WriteString(fmt.Sprintf("%d: %s\n", lineNum, lineText))

			// Show context if requested
			if contextLines > 0 {
				// Context lines are included in the ripgrep -C output as separate "context" type entries
				// For simplicity, we just show the match line here
			}
		} else if match.Type == "context" && contextLines > 0 {
			lineNum := match.Data.LineNumber
			lineText := strings.TrimRight(match.Data.Lines.Text, "\n")
			result.WriteString(fmt.Sprintf("%d: %s\n", lineNum, lineText))
		}
	}

	if result.Len() == 0 {
		return "No matches found", nil
	}

	return result.String(), nil
}
