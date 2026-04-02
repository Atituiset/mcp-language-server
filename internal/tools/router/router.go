package router

import (
	"context"
	"strings"
	"sync"

	"github.com/isaacphi/mcp-language-server/internal/tools"
)

type Router struct {
	workspaceDir string
}

type SearchOptions struct {
	Query    string
	Strategy string // "auto", "text", "ast", "symbol"
	Intent   string
	FilePath string
	Language string
}

type SearchResult struct {
	Layer   string
	Content string
	Count   int
}

func NewRouter(workspaceDir string) *Router {
	return &Router{workspaceDir: workspaceDir}
}

func (r *Router) Search(ctx context.Context, opts SearchOptions) ([]SearchResult, error) {
	strategy := opts.Strategy
	if strategy == "" {
		strategy = "auto"
	}

	switch strategy {
	case "text":
		return r.searchText(ctx, opts)
	case "ast":
		return r.searchAST(ctx, opts)
	case "symbol":
		return r.searchSymbol(ctx, opts)
	case "auto":
		return r.searchAuto(ctx, opts)
	default:
		return r.searchAuto(ctx, opts)
	}
}

func (r *Router) searchText(ctx context.Context, opts SearchOptions) ([]SearchResult, error) {
	rgOpts := tools.RipgrepOptions{
		MaxCount: 100,
	}
	result, err := tools.SearchCode(ctx, r.workspaceDir, opts.Query, rgOpts)
	if err != nil {
		return nil, err
	}
	return []SearchResult{
		{Layer: "text", Content: result, Count: countLines(result)},
	}, nil
}

func (r *Router) searchAST(ctx context.Context, opts SearchOptions) ([]SearchResult, error) {
	language := opts.Language
	if language == "" {
		language = "cpp"
	}

	result, err := tools.RunTreesitterQuery(ctx, r.workspaceDir, opts.Query, opts.FilePath, language)
	if err != nil {
		return nil, err
	}
	return []SearchResult{
		{Layer: "ast", Content: result, Count: countLines(result)},
	}, nil
}

func (r *Router) searchSymbol(ctx context.Context, opts SearchOptions) ([]SearchResult, error) {
	// Use ripgrep for symbol-like search as a fallback
	// LSP symbol search requires a symbol name, not a pattern
	rgOpts := tools.RipgrepOptions{
		MaxCount: 100,
		CaseSensitive: true,
	}
	result, err := tools.SearchCode(ctx, r.workspaceDir, opts.Query, rgOpts)
	if err != nil {
		return nil, err
	}
	return []SearchResult{
		{Layer: "symbol", Content: result, Count: countLines(result)},
	}, nil
}

func (r *Router) searchAuto(ctx context.Context, opts SearchOptions) ([]SearchResult, error) {
	// Route by intent hint
	if opts.Intent != "" {
		strategy := r.routeByIntent(opts.Intent)
		switch strategy {
		case "text":
			return r.searchText(ctx, opts)
		case "ast":
			return r.searchAST(ctx, opts)
		case "symbol":
			return r.searchSymbol(ctx, opts)
		}
	}

	// No intent or couldn't route - search all layers in parallel
	return r.searchAll(ctx, opts)
}

func (r *Router) searchAll(ctx context.Context, opts SearchOptions) ([]SearchResult, error) {
	var wg sync.WaitGroup
	var mu sync.Mutex
	results := []SearchResult{}
	errors := []error{}

	// Search text layer
	wg.Add(1)
	go func() {
		defer wg.Done()
		result, err := r.searchText(ctx, opts)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			errors = append(errors, err)
		} else {
			results = append(results, result...)
		}
	}()

	// Search AST layer
	wg.Add(1)
	go func() {
		defer wg.Done()
		result, err := r.searchAST(ctx, opts)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			errors = append(errors, err)
		} else {
			results = append(results, result...)
		}
	}()

	// Search symbol layer
	wg.Add(1)
	go func() {
		defer wg.Done()
		result, err := r.searchSymbol(ctx, opts)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			errors = append(errors, err)
		} else {
			results = append(results, result...)
		}
	}()

	wg.Wait()

	if len(results) == 0 && len(errors) > 0 {
		// Return first error if all layers failed
		return nil, errors[0]
	}

	return results, nil
}

func (r *Router) routeByIntent(intent string) string {
	intent = strings.ToLower(intent)

	textKeywords := []string{"todo", "fixme", "comment", "string", "text", "pattern", "word", "find text"}
	astKeywords := []string{"function", "struct", "class", "node", "ast", "syntax", "definition", "declare"}
	symbolKeywords := []string{"symbol", "reference", "call", "usage", "import", "type", "variable"}

	for _, kw := range textKeywords {
		if strings.Contains(intent, kw) {
			return "text"
		}
	}

	for _, kw := range astKeywords {
		if strings.Contains(intent, kw) {
			return "ast"
		}
	}

	for _, kw := range symbolKeywords {
		if strings.Contains(intent, kw) {
			return "symbol"
		}
	}

	return ""
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}
