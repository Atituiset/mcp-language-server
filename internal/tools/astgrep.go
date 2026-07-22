package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// AstGrepOptions contains options for ast-grep search.
type AstGrepOptions struct {
	Language string // language for parsing (e.g. "c", "cpp", "go", "python", "rust")
	Rewrite  string // optional rewrite pattern
	// Files, when non-empty, restricts the search to these explicit paths
	// instead of the whole workspace directory.
	Files []string
}

// AstGrepMatch is a single ast-grep match with its position and byte offset.
type AstGrepMatch struct {
	Path      string // absolute or relative path (as returned by sg)
	Line      int    // 1-indexed line number
	Column    int    // 1-indexed column number
	StartByte int    // byte offset of match start
	EndByte   int    // byte offset of match end
	Text      string // the matched source text
	Lines     string // the full line containing the match
	Language  string // detected language
}

// SearchAstGrep runs ast-grep in the workspace directory with the given pattern.
func SearchAstGrep(ctx context.Context, workspaceDir, pattern string, opts AstGrepOptions) (string, error) {
	output, err := runAstGrep(ctx, workspaceDir, pattern, opts)
	if err != nil {
		return "", err
	}
	return parseAstGrepOutput(output), nil
}

// SearchAstGrepMatches is the structured variant of SearchAstGrep: it returns
// parsed matches instead of formatted text, for normalization into atoms.
func SearchAstGrepMatches(ctx context.Context, workspaceDir, pattern string, opts AstGrepOptions) ([]AstGrepMatch, error) {
	output, err := runAstGrep(ctx, workspaceDir, pattern, opts)
	if err != nil {
		return nil, err
	}
	return parseAstGrepMatches(output), nil
}

func runAstGrep(ctx context.Context, workspaceDir, pattern string, opts AstGrepOptions) ([]byte, error) {
	sgBin := FindAstGrep()
	if sgBin == "" {
		return nil, fmt.Errorf("ast-grep (sg) not found in PATH")
	}

	args := []string{"run", "--json"}
	if opts.Language != "" {
		args = append(args, "--lang", opts.Language)
	}
	args = append(args, "--pattern", pattern)

	cmd := exec.CommandContext(ctx, sgBin, args...)
	cmd.Dir = workspaceDir

	if len(opts.Files) > 0 {
		// Relative-ize against workspaceDir so ast-grep output uses short paths.
		var relFiles []string
		for _, f := range opts.Files {
			if rel, err := filepath.Rel(workspaceDir, f); err == nil {
				relFiles = append(relFiles, rel)
			} else {
				relFiles = append(relFiles, f)
			}
		}
		cmd.Args = append(cmd.Args, relFiles...)
	}

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	output, err := cmd.Output()
	if err != nil {
		// sg exit 1 = no matches (not a real error)
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 && stderr.Len() == 0 {
			return nil, nil
		}
		if stderr.Len() > 0 {
			return nil, fmt.Errorf("ast-grep error: %s", stderr.String())
		}
		return nil, fmt.Errorf("ast-grep failed: %v", err)
	}

	return output, nil
}

// FindAstGrep returns the path to the ast-grep binary (prefers "sg").
func FindAstGrep() string {
	for _, name := range []string{"sg", "ast-grep"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

// astGrepJSONMatch is the JSON shape of a single ast-grep --json output entry.
type astGrepJSONMatch struct {
	Text      string `json:"text"`
	Range     astGrepRange `json:"range"`
	File      string `json:"file"`
	Lines     string `json:"lines"`
	Language  string `json:"language"`
}

type astGrepRange struct {
	ByteOffset struct {
		Start int `json:"start"`
		End   int `json:"end"`
	} `json:"byteOffset"`
	Start struct {
		Line   int `json:"line"`
		Column int `json:"column"`
	} `json:"start"`
	End struct {
		Line   int `json:"line"`
		Column int `json:"column"`
	} `json:"end"`
}

// parseAstGrepMatches converts ast-grep --json output into structured matches.
func parseAstGrepMatches(output []byte) []AstGrepMatch {
	if len(output) == 0 {
		return nil
	}

	var raw []astGrepJSONMatch
	if err := json.Unmarshal(output, &raw); err != nil {
		return nil
	}

	matches := make([]AstGrepMatch, 0, len(raw))
	for _, m := range raw {
		matches = append(matches, AstGrepMatch{
			Path:      m.File,
			Line:      m.Range.Start.Line + 1,   // sg uses 0-indexed lines
			Column:    m.Range.Start.Column + 1,  // sg uses 0-indexed columns
			StartByte: m.Range.ByteOffset.Start,
			EndByte:   m.Range.ByteOffset.End,
			Text:      m.Text,
			Lines:     strings.TrimRight(m.Lines, "\n"),
			Language:  m.Language,
		})
	}
	return matches
}

func parseAstGrepOutput(output []byte) string {
	matches := parseAstGrepMatches(output)
	if len(matches) == 0 {
		return "No matches found"
	}

	var result strings.Builder
	result.WriteString(fmt.Sprintf("Found %d matches:\n\n", len(matches)))

	currentFile := ""
	for _, m := range matches {
		if m.Path != currentFile {
			if currentFile != "" {
				result.WriteString("\n")
			}
			result.WriteString(fmt.Sprintf("=== %s ===\n", m.Path))
			currentFile = m.Path
		}
		result.WriteString(fmt.Sprintf("%d: %s\n", m.Line, m.Lines))
	}

	return result.String()
}

// DetectAstGrepLang maps a file extension to an ast-grep language name.
func DetectAstGrepLang(filePath string) string {
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".c", ".h":
		return "c"
	case ".cpp", ".cc", ".cxx", ".hpp", ".hxx":
		return "cpp"
	case ".go":
		return "go"
	case ".py":
		return "python"
	case ".rs":
		return "rust"
	case ".ts", ".tsx":
		return "typescript"
	case ".js", ".jsx":
		return "javascript"
	case ".java":
		return "java"
	case ".rb":
		return "ruby"
	case ".cs":
		return "csharp"
	case ".swift":
		return "swift"
	case ".kt", ".kts":
		return "kotlin"
	case ".lua":
		return "lua"
	default:
		return ""
	}
}
