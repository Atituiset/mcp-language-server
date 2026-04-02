package treesitter

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/c"
	"github.com/smacker/go-tree-sitter/cpp"
)

type Parser struct {
	p    *sitter.Parser
	lang *sitter.Language
}

func NewCParser() *Parser {
	p := sitter.NewParser()
	lang := c.GetLanguage()
	p.SetLanguage(lang)
	return &Parser{p: p, lang: lang}
}

func NewCppParser() *Parser {
	p := sitter.NewParser()
	lang := cpp.GetLanguage()
	p.SetLanguage(lang)
	return &Parser{p: p, lang: lang}
}

func NewParser(language string) *Parser {
	switch strings.ToLower(language) {
	case "c", "h":
		return NewCParser()
	case "cpp", "cxx", "cc", "hpp", "hxx":
		return NewCppParser()
	default:
		return NewCppParser()
	}
}

func DetectLanguage(filePath string) string {
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".c", ".h":
		return "c"
	case ".cpp", ".cxx", ".cc", ".hpp", ".hxx":
		return "cpp"
	default:
		return "cpp"
	}
}

func (p *Parser) Language() *sitter.Language {
	return p.lang
}

func (p *Parser) ParseFile(ctx context.Context, path string) (*sitter.Tree, []byte, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}

	tree := p.p.Parse(nil, content)
	if tree == nil {
		return nil, content, ErrParseFailed
	}
	return tree, content, nil
}

func (p *Parser) ParseContent(ctx context.Context, content []byte) (*sitter.Tree, error) {
	tree := p.p.Parse(nil, content)
	if tree == nil {
		return nil, ErrParseFailed
	}
	return tree, nil
}

func (p *Parser) Close() {
	if p.p != nil {
		p.p.Close()
	}
}

var ErrParseFailed = &ParseError{Message: "failed to parse content"}

type ParseError struct {
	Message string
}

func (e *ParseError) Error() string {
	return e.Message
}
