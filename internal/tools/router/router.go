package router

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/isaacphi/mcp-language-server/internal/lsp"
	"github.com/isaacphi/mcp-language-server/internal/protocol"
	"github.com/isaacphi/mcp-language-server/internal/tools"
	"github.com/isaacphi/mcp-language-server/internal/tools/cache"
)

type Router struct {
	workspaceDir string
	lspClient    *lsp.Client
	cache        *cache.SearchResultCache
}

type SearchOptions struct {
	Query    string
	Strategy string
	Intent   string
	FilePath string
	Language string
}

type SearchResult struct {
	Layer   string
	Content string
	Count   int
}

const defaultCacheTTL = 300

func NewRouter(workspaceDir string) *Router {
	return &Router{
		workspaceDir: workspaceDir,
		cache:        cache.NewSearchResultCache(time.Duration(defaultCacheTTL) * time.Second),
	}
}

func NewRouterWithCache(workspaceDir string, cacheTTLSeconds int64) *Router {
	return &Router{
		workspaceDir: workspaceDir,
		cache:        cache.NewSearchResultCache(time.Duration(cacheTTLSeconds) * time.Second),
	}
}

func NewRouterWithClient(workspaceDir string, client *lsp.Client) *Router {
	return &Router{
		workspaceDir: workspaceDir,
		lspClient:    client,
		cache:        cache.NewSearchResultCache(time.Duration(defaultCacheTTL) * time.Second),
	}
}

func (r *Router) Search(ctx context.Context, opts SearchOptions) ([]SearchResult, error) {
	strategy := opts.Strategy
	if strategy == "" {
		strategy = "auto"
	}

	cacheKey := cache.SearchCacheKey(opts.Query, strategy, opts.FilePath, opts.Language, opts.Intent)

	if r.cache != nil {
		if cached, found := r.cache.Get(cacheKey); found {
			if results, ok := cached.([]SearchResult); ok {
				return results, nil
			}
		}
	}

	var results []SearchResult
	var err error

	switch strategy {
	case "text":
		results, err = r.searchText(ctx, opts)
	case "ast":
		results, err = r.searchAST(ctx, opts)
	case "symbol":
		results, err = r.searchSymbol(ctx, opts)
	case "auto":
		results, err = r.searchAuto(ctx, opts)
	default:
		results, err = r.searchAuto(ctx, opts)
	}

	if err == nil {
		for i := range results {
			hint := results[i].Layer
			if hint == "symbol-fallback-text" {
				hint = "symbol"
			}
			results[i].Content = truncateLayerContent(results[i].Content, hint)
		}
	}

	if err == nil && r.cache != nil {
		r.cache.Set(cacheKey, results, 0)
	}

	return results, err
}

func (r *Router) ClearCache() {
	if r.cache != nil {
		r.cache.Clear()
	}
}

func (r *Router) CacheSize() int {
	if r.cache != nil {
		return r.cache.Size()
	}
	return 0
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
	if r.lspClient != nil {
		result, err := r.lspClient.Symbol(ctx, protocol.WorkspaceSymbolParams{Query: opts.Query})
		if err == nil {
			symbols, parseErr := result.Results()
			if parseErr == nil && len(symbols) > 0 {
				content := formatSymbolResults(symbols)
				return []SearchResult{
					{Layer: "symbol", Content: content, Count: len(symbols)},
				}, nil
			}
		}
	}

	rgOpts := tools.RipgrepOptions{
		MaxCount:      100,
		CaseSensitive: true,
	}
	result, err := tools.SearchCode(ctx, r.workspaceDir, opts.Query, rgOpts)
	if err != nil {
		return nil, err
	}
	content := "WARNING: LSP unavailable, results are plain text matches\n" + result
	return []SearchResult{
		{Layer: "symbol-fallback-text", Content: content, Count: countLines(result)},
	}, nil
}

func (r *Router) searchAuto(ctx context.Context, opts SearchOptions) ([]SearchResult, error) {
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

	return r.searchAll(ctx, opts)
}

func (r *Router) searchAll(ctx context.Context, opts SearchOptions) ([]SearchResult, error) {
	var wg sync.WaitGroup
	var mu sync.Mutex
	results := []SearchResult{}
	errors := []error{}

	searchFns := []func(context.Context, SearchOptions) ([]SearchResult, error){
		r.searchText,
		r.searchAST,
		r.searchSymbol,
	}

	for _, fn := range searchFns {
		wg.Add(1)
		go func(f func(context.Context, SearchOptions) ([]SearchResult, error)) {
			defer wg.Done()
			result, err := f(ctx, opts)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errors = append(errors, err)
			} else {
				results = append(results, result...)
			}
		}(fn)
	}

	wg.Wait()

	if len(results) == 0 && len(errors) > 0 {
		return nil, errors[0]
	}

	return results, nil
}

func (r *Router) routeByIntent(intent string) string {
	intent = strings.ToLower(intent)

	textKeywords := []string{"todo", "fixme", "comment", "string", "text", "pattern", "word", "find text", "注释", "文本", "待办"}
	astKeywords := []string{"function", "struct", "class", "node", "ast", "syntax", "函数", "结构体", "语法"}
	symbolKeywords := []string{"symbol", "reference", "call", "usage", "import", "type", "variable", "definition", "declare", "定义", "引用", "调用", "声明"}

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

func formatSymbolResults(symbols []protocol.WorkspaceSymbolResult) string {
	var b strings.Builder
	for _, sym := range symbols {
		loc := sym.GetLocation()
		path := loc.URI.Path()
		line := loc.Range.Start.Line + 1
		fmt.Fprintf(&b, "%s:%d: %s\n", path, line, sym.GetName())
	}
	return b.String()
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

const (
	maxLayerLines = 50
	maxLayerBytes = 4 * 1024
)

// truncateLayerContent caps a single layer's output at maxLayerLines lines and
// maxLayerBytes bytes, whichever hits first. When truncation occurs, a hint
// telling the caller how to narrow the search is appended.
func truncateLayerContent(content, strategy string) string {
	lines := strings.Split(content, "\n")
	truncated := false
	if len(lines) > maxLayerLines {
		lines = lines[:maxLayerLines]
		truncated = true
	}

	out := strings.Join(lines, "\n")
	if len(out) > maxLayerBytes {
		out = out[:maxLayerBytes]
		if idx := strings.LastIndex(out, "\n"); idx > 0 {
			out = out[:idx]
		}
		truncated = true
	}

	if !truncated {
		return content
	}

	omitted := countLines(content) - countLines(out)
	if omitted < 1 {
		omitted = 1
	}
	return fmt.Sprintf("%s\n... [truncated, %d more lines, use strategy=%s with filePath to narrow]\n", out, omitted, strategy)
}
