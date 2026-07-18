package router

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/isaacphi/mcp-language-server/internal/lsp"
	"github.com/isaacphi/mcp-language-server/internal/protocol"
	"github.com/isaacphi/mcp-language-server/internal/tools"
	"github.com/isaacphi/mcp-language-server/internal/tools/atom"
	"github.com/isaacphi/mcp-language-server/internal/tools/cache"
	"github.com/isaacphi/mcp-language-server/internal/tools/treesitter"
)

type Router struct {
	workspaceDir string
	lspClient    *lsp.Client
	cache        *cache.SearchResultCache
	includeOnce  sync.Once
	includeMap   *tools.IncludeMap
}

type SearchOptions struct {
	Query    string
	Strategy string
	Intent   string
	FilePath string
	Language string

	// Text strategy (ripgrep) options; zero values fall back to defaults.
	MaxCount      int
	ContextLines  int
	CaseSensitive bool
	WholeWord     bool
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

func NewRouterWithClient(workspaceDir string, client *lsp.Client, cacheTTLSeconds ...int64) *Router {
	ttl := int64(defaultCacheTTL)
	if len(cacheTTLSeconds) > 0 && cacheTTLSeconds[0] > 0 {
		ttl = cacheTTLSeconds[0]
	}
	return &Router{
		workspaceDir: workspaceDir,
		lspClient:    client,
		cache:        cache.NewSearchResultCache(time.Duration(ttl) * time.Second),
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
			if strings.HasPrefix(results[i].Layer, "unified") {
				continue // unified output carries its own budget cap
			}
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

// getIncludeMap lazily loads the compile_commands.json include mapping.
func (r *Router) getIncludeMap() *tools.IncludeMap {
	r.includeOnce.Do(func() {
		if m, err := tools.LoadIncludeMap(r.workspaceDir); err == nil && m.Size() > 0 {
			r.includeMap = m
		}
	})
	return r.includeMap
}

// scopedRipgrepOptions narrows a ripgrep invocation to the include
// neighborhood of opts.FilePath when the include map knows the file.
// It returns the (possibly scoped) options and the scope size (0 = unscoped).
func (r *Router) scopedRipgrepOptions(opts SearchOptions, base tools.RipgrepOptions) (tools.RipgrepOptions, int) {
	if m := r.getIncludeMap(); m != nil && opts.FilePath != "" {
		if nb := m.Neighborhood(opts.FilePath); len(nb) > 0 {
			if len(nb) > maxScopeFiles {
				nb = nb[:maxScopeFiles]
			}
			base.Files = nb
			return base, len(nb)
		}
	}
	return base, 0
}

// textRipgrepOptions resolves the file scope for text searches: with an
// anchor filePath, the include map decides where to search — the file's
// include neighborhood when it discriminates, the file itself otherwise.
func (r *Router) textRipgrepOptions(opts SearchOptions, base tools.RipgrepOptions) (tools.RipgrepOptions, int) {
	if opts.FilePath == "" {
		return base, 0
	}
	if scoped, n := r.scopedRipgrepOptions(opts, base); n > 0 {
		return scoped, n
	}
	base.Files = []string{opts.FilePath}
	return base, 1
}

func (r *Router) CacheSize() int {
	if r.cache != nil {
		return r.cache.Size()
	}
	return 0
}

func (r *Router) searchText(ctx context.Context, opts SearchOptions) ([]SearchResult, error) {
	rgOpts, scoped := r.textRipgrepOptions(opts, tools.RipgrepOptions{
		MaxCount:      opts.MaxCount,
		ContextLines:  opts.ContextLines,
		CaseSensitive: opts.CaseSensitive,
		WholeWord:     opts.WholeWord,
	})
	if rgOpts.MaxCount <= 0 {
		rgOpts.MaxCount = 100
	}
	result, err := tools.SearchCode(ctx, r.workspaceDir, opts.Query, rgOpts)
	if err != nil {
		return nil, err
	}
	if scoped > 0 {
		result = fmt.Sprintf("NOTE: scoped to %d file(s) via include map\n%s", scoped, result)
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

	// When anchored on a file, expand the query to its include neighborhood
	// (bounded) so macro-gated code near the anchor is covered.
	if opts.FilePath != "" {
		if m := r.getIncludeMap(); m != nil {
			if nb := m.Neighborhood(opts.FilePath); len(nb) > 0 {
				if len(nb) > maxASTScopeFiles {
					nb = nb[:maxASTScopeFiles]
				}
				var all []treesitter.QueryResult
				for _, f := range nb {
					if res, err := tools.RunTreesitterQueryResults(ctx, r.workspaceDir, opts.Query, f, language); err == nil {
						all = append(all, res...)
					}
				}
				content := fmt.Sprintf("NOTE: expanded to %d files via include map\n%s", len(nb), tools.FormatQueryResults(all))
				return []SearchResult{
					{Layer: "ast", Content: content, Count: len(all)},
				}, nil
			}
		}
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

	rgOpts, scoped := r.scopedRipgrepOptions(opts, tools.RipgrepOptions{
		MaxCount:      100,
		CaseSensitive: true,
	})
	result, err := tools.SearchCode(ctx, r.workspaceDir, opts.Query, rgOpts)
	if err != nil {
		return nil, err
	}
	content := "WARNING: LSP unavailable, results are plain text matches"
	if scoped > 0 {
		content += fmt.Sprintf(" (scoped to %d files via include map)", scoped)
	}
	content += "\n" + result
	return []SearchResult{
		{Layer: "symbol-fallback-text", Content: content, Count: countLines(result)},
	}, nil
}

func (r *Router) searchAuto(ctx context.Context, opts SearchOptions) ([]SearchResult, error) {
	if opts.Intent != "" {
		strategy := r.routeByIntent(opts.Intent)
		switch strategy {
		case "text", "ast", "symbol":
			return r.searchLayerUnified(ctx, opts, strategy)
		}
	}

	return r.searchAll(ctx, opts)
}

// searchLayerUnified runs a single intent-routed layer through the same
// normalize/merge/dedup/crop pipeline as searchAll, keeping auto-path
// output semantics consistent regardless of intent routing. Explicit
// strategy=text|ast|symbol calls keep the raw single-layer format.
func (r *Router) searchLayerUnified(ctx context.Context, opts SearchOptions, layer string) ([]SearchResult, error) {
	var atoms []atom.CodeAtom
	warning := ""

	switch layer {
	case "text":
		rgOpts, _ := r.textRipgrepOptions(opts, tools.RipgrepOptions{
			MaxCount:      opts.MaxCount,
			ContextLines:  opts.ContextLines,
			CaseSensitive: opts.CaseSensitive,
			WholeWord:     opts.WholeWord,
		})
		if rgOpts.MaxCount <= 0 {
			rgOpts.MaxCount = 100
		}
		matches, err := tools.SearchCodeMatches(ctx, r.workspaceDir, opts.Query, rgOpts)
		if err != nil {
			return nil, err
		}
		atoms = atomsFromTextMatches(matches, "rg")
	case "ast":
		language := opts.Language
		if language == "" {
			language = "cpp"
		}
		results, err := tools.RunTreesitterQueryResults(ctx, r.workspaceDir, opts.Query, opts.FilePath, language)
		if err != nil {
			return nil, err
		}
		atoms = atomsFromTreeSitter(results)
	case "symbol":
		symbols, fallback, err := r.querySymbols(ctx, opts.Query)
		if err != nil {
			return nil, err
		}
		atoms = atomsFromSymbols(symbols, map[string][]byte{})
		if fallback {
			fbOpts, scoped := r.scopedRipgrepOptions(opts, tools.RipgrepOptions{
				MaxCount:      100,
				CaseSensitive: true,
			})
			fbMatches, fbErr := tools.SearchCodeMatches(ctx, r.workspaceDir, opts.Query, fbOpts)
			if fbErr != nil {
				return nil, fbErr
			}
			atoms = append(atoms, atomsFromTextMatches(fbMatches, "rg(lsp-fallback)")...)
			warning = "WARNING: LSP unavailable, results are plain text matches"
			if scoped > 0 {
				warning += fmt.Sprintf(" (scoped to %d files via include map)", scoped)
			}
		}
	default:
		return r.searchAll(ctx, opts)
	}

	if len(atoms) == 0 {
		return []SearchResult{{Layer: "unified-" + layer, Content: "No results found", Count: 0}}, nil
	}

	atoms = atom.MergePhysical(atoms)
	atoms = atom.DedupSemantic(atoms)
	kept, stats := atom.CropBudget(atoms, unifiedBudgetBytes)

	content := atom.RenderWithLabel(kept, stats, "unified-"+layer)
	if warning != "" {
		content = warning + "\n" + content
	}
	return []SearchResult{{Layer: "unified-" + layer, Content: content, Count: stats.Total - stats.Dropped}}, nil
}

// unifiedBudgetBytes is the payload budget for searchAll output
// (docs/code-atom-ir.md §4). Rendered overhead (file headers, per-atom
// tags) adds ~25%, so 8KB of payload stays well under the 12KB total cap.
const unifiedBudgetBytes = 8 * 1024

const (
	// maxScopeFiles caps include-map-scoped ripgrep fallbacks.
	maxScopeFiles = 400
	// maxASTScopeFiles caps include-map-expanded tree-sitter queries.
	maxASTScopeFiles = 20
)

// searchAll fans out to all three layers, then normalizes the heterogeneous
// results into CodeAtoms and applies merge/dedup/budget-crop
// (docs/code-atom-ir.md §1-§4) instead of raw concatenation.
func (r *Router) searchAll(ctx context.Context, opts SearchOptions) ([]SearchResult, error) {
	var wg sync.WaitGroup
	var mu sync.Mutex

	var textMatches []tools.TextMatch
	var astResults []treesitter.QueryResult
	var symbols []protocol.WorkspaceSymbolResult
	var symbolFallback bool
	errors := []error{}

	wg.Add(3)
	go func() {
		defer wg.Done()
		rgOpts, _ := r.textRipgrepOptions(opts, tools.RipgrepOptions{
			MaxCount:      opts.MaxCount,
			ContextLines:  opts.ContextLines,
			CaseSensitive: opts.CaseSensitive,
			WholeWord:     opts.WholeWord,
		})
		if rgOpts.MaxCount <= 0 {
			rgOpts.MaxCount = 100
		}
		m, err := tools.SearchCodeMatches(ctx, r.workspaceDir, opts.Query, rgOpts)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			errors = append(errors, err)
		} else {
			textMatches = m
		}
	}()
	go func() {
		defer wg.Done()
		language := opts.Language
		if language == "" {
			language = "cpp"
		}
		res, err := tools.RunTreesitterQueryResults(ctx, r.workspaceDir, opts.Query, opts.FilePath, language)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			errors = append(errors, err)
		} else {
			astResults = res
		}
	}()
	go func() {
		defer wg.Done()
		syms, fallback, err := r.querySymbols(ctx, opts.Query)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			errors = append(errors, err)
		} else {
			symbols = syms
			symbolFallback = fallback
		}
	}()
	wg.Wait()

	// Normalize sequentially (file reads for byte-offset conversion are cached).
	var atoms []atom.CodeAtom
	atoms = append(atoms, atomsFromTextMatches(textMatches, "rg")...)
	atoms = append(atoms, atomsFromTreeSitter(astResults)...)
	fileCache := map[string][]byte{}
	atoms = append(atoms, atomsFromSymbols(symbols, fileCache)...)

	fallbackScoped := 0
	if symbolFallback {
		fbOpts, fbScoped := r.scopedRipgrepOptions(opts, tools.RipgrepOptions{
			MaxCount:      100,
			CaseSensitive: true,
		})
		fbMatches, err := tools.SearchCodeMatches(ctx, r.workspaceDir, opts.Query, fbOpts)
		if err != nil {
			errors = append(errors, err)
		} else {
			atoms = append(atoms, atomsFromTextMatches(fbMatches, "rg(lsp-fallback)")...)
		}
		fallbackScoped = fbScoped
	}

	if len(atoms) == 0 {
		if len(errors) > 0 {
			return nil, errors[0]
		}
		return []SearchResult{{Layer: "unified", Content: "No results found", Count: 0}}, nil
	}

	atoms = atom.MergePhysical(atoms)
	atoms = atom.DedupSemantic(atoms)
	kept, stats := atom.CropBudget(atoms, unifiedBudgetBytes)

	content := atom.Render(kept, stats)
	if symbolFallback {
		warning := "WARNING: LSP unavailable, results are plain text matches"
		if fallbackScoped > 0 {
			warning += fmt.Sprintf(" (scoped to %d files via include map)", fallbackScoped)
		}
		content = warning + "\n" + content
	}

	return []SearchResult{{Layer: "unified", Content: content, Count: stats.Total - stats.Dropped}}, nil
}

// querySymbols queries the LSP workspace/symbol endpoint. fallback=true
// means the symbol layer is unavailable (no client, error, or empty) and
// the caller should degrade to text search.
func (r *Router) querySymbols(ctx context.Context, query string) ([]protocol.WorkspaceSymbolResult, bool, error) {
	if r.lspClient != nil {
		result, err := r.lspClient.Symbol(ctx, protocol.WorkspaceSymbolParams{Query: query})
		if err != nil {
			return nil, false, err
		}
		symbols, parseErr := result.Results()
		if parseErr != nil {
			return nil, false, parseErr
		}
		if len(symbols) > 0 {
			return symbols, false, nil
		}
	}
	return nil, true, nil
}

// atomsFromTextMatches normalizes ripgrep matches (docs §1: line-level plain
// text, physical coordinates only, lowest priority).
func atomsFromTextMatches(matches []tools.TextMatch, source string) []atom.CodeAtom {
	atoms := make([]atom.CodeAtom, 0, len(matches))
	for _, m := range matches {
		text := strings.TrimSpace(m.Text)
		atoms = append(atoms, atom.CodeAtom{
			SemanticID:  fmt.Sprintf("%s:%d", m.Path, m.Offset),
			Name:        truncateLine(text, 60),
			Kind:        atom.KindSnippet,
			FilePath:    m.Path,
			StartByte:   m.Offset,
			EndByte:     m.Offset + len(m.Text),
			FullContent: m.Text,
			Signature:   truncateLine(text, 120),
			Reference:   fmt.Sprintf("%s:%d: %s", m.Path, m.Line, truncateLine(text, 60)),
			SourceTool:  source,
			Priority:    1,
		})
	}
	return atoms
}

// atomsFromTreeSitter normalizes AST nodes (docs §1: local syntax-tree nodes
// with native byte ranges and a temporary ID from path+node-type+offset).
func atomsFromTreeSitter(results []treesitter.QueryResult) []atom.CodeAtom {
	atoms := make([]atom.CodeAtom, 0, len(results))
	for _, r := range results {
		atoms = append(atoms, atom.CodeAtom{
			SemanticID:  fmt.Sprintf("%s:%s:%d", r.FilePath, r.NodeType, r.StartByte),
			Name:        r.Capture,
			Kind:        atomKindFromNodeType(r.NodeType),
			FilePath:    r.FilePath,
			StartByte:   int(r.StartByte),
			EndByte:     int(r.EndByte),
			FullContent: r.Content,
			Signature:   truncateLine(strings.TrimSpace(r.Content), 120),
			Reference:   fmt.Sprintf("%s:%d: [%s] %s", r.FilePath, r.Line, r.Capture, truncateLine(strings.TrimSpace(r.Content), 60)),
			SourceTool:  "tree-sitter",
			Priority:    2,
		})
	}
	return atoms
}

// atomsFromSymbols normalizes LSP symbols (docs §1: global semantic symbols;
// Phase 1 uses FQN-style IDs since LSP does not expose USRs, and carries no
// FullContent to avoid per-symbol definition round-trips).
func atomsFromSymbols(symbols []protocol.WorkspaceSymbolResult, fileCache map[string][]byte) []atom.CodeAtom {
	atoms := make([]atom.CodeAtom, 0, len(symbols))
	for _, sym := range symbols {
		loc := sym.GetLocation()
		path := loc.URI.Path()
		line := int(loc.Range.Start.Line) + 1
		name := sym.GetName()
		offset := byteOffsetOfLine(path, line-1, fileCache)
		endByte := -1
		if offset >= 0 {
			endByte = offset + len(name)
		}
		atoms = append(atoms, atom.CodeAtom{
			SemanticID: fmt.Sprintf("%s@%s", name, path),
			Name:       name,
			Kind:       atom.KindSymbol,
			FilePath:   path,
			StartByte:  offset,
			EndByte:    endByte,
			Signature:  name,
			Reference:  fmt.Sprintf("%s:%d: %s", path, line, name),
			SourceTool: "clangd",
			Priority:   3,
		})
	}
	return atoms
}

// byteOffsetOfLine converts a 0-indexed line number to a byte offset.
// Returns -1 when the file cannot be read or the line is out of range.
func byteOffsetOfLine(path string, line int, fileCache map[string][]byte) int {
	if line < 0 {
		return -1
	}
	content, ok := fileCache[path]
	if !ok {
		data, err := os.ReadFile(path)
		if err != nil {
			fileCache[path] = nil
			return -1
		}
		content = data
		fileCache[path] = content
	}
	if content == nil {
		return -1
	}
	offset := 0
	for l := 0; l < line; l++ {
		idx := bytes.IndexByte(content[offset:], '\n')
		if idx < 0 {
			return -1
		}
		offset += idx + 1
	}
	return offset
}

// atomKindFromNodeType maps tree-sitter node types to atom kinds.
func atomKindFromNodeType(nodeType string) atom.Kind {
	switch nodeType {
	case "function_definition", "function_declarator", "method_definition":
		return atom.KindFunction
	case "struct_specifier", "class_specifier", "union_specifier", "enum_specifier":
		return atom.KindStruct
	case "preproc_def", "preproc_function_def":
		return atom.KindMacro
	default:
		return atom.KindSnippet
	}
}

func truncateLine(s string, maxLen int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
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
