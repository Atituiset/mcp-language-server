package router

import (
	"context"
	"strings"
	"sync"

	"github.com/isaacphi/mcp-language-server/internal/tools"
)

// Router 搜索路由器，根据策略将搜索请求分发到不同的搜索层
//
// 支持三层搜索架构：
//   - L1 (text): ripgrep 文本搜索，最快速
//   - L2 (ast): tree-sitter AST 查询，支持结构化搜索
//   - L3 (symbol): LSP 符号搜索，语义理解
type Router struct {
	workspaceDir string // 工作区目录路径
}

// SearchOptions 搜索选项
type SearchOptions struct {
	Query    string // 搜索查询字符串
	Strategy string // 搜索策略: "auto", "text", "ast", "symbol"
	Intent   string // 意图提示: "todo", "function", "definition" 等
	FilePath string // 限定文件路径
	Language string // 语言类型: "c", "cpp", "auto"
}

// SearchResult 单个搜索层的结果
type SearchResult struct {
	Layer   string // 所属层级: "text", "ast", "symbol"
	Content string // 格式化后的结果内容
	Count   int    // 结果数量
}

// NewRouter 创建新的搜索路由器
func NewRouter(workspaceDir string) *Router {
	return &Router{workspaceDir: workspaceDir}
}

// Search 执行统一搜索
//
// 参数:
//   - ctx: 上下文
//   - opts: 搜索选项
//
// 返回: 所有匹配层的结果列表
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

// searchText 使用 L1 ripgrep 进行文本搜索
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

// searchAST 使用 L2 tree-sitter 进行 AST 查询
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

// searchSymbol 使用 L3 LSP 进行符号搜索
func (r *Router) searchSymbol(ctx context.Context, opts SearchOptions) ([]SearchResult, error) {
	// 使用 ripgrep 作为符号搜索的后备方案
	// LSP 符号搜索需要精确的符号名称
	rgOpts := tools.RipgrepOptions{
		MaxCount:      100,
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

// searchAuto 自动路由，根据意图提示选择合适的搜索层
//
// 路由规则：
//   - 包含 "todo", "comment", "string" 等关键词 → text (L1)
//   - 包含 "function", "struct", "class", "node" 等关键词 → ast (L2)
//   - 包含 "definition", "reference", "type" 等关键词 → symbol (L3)
//   - 无意图提示 → 并行搜索所有层
func (r *Router) searchAuto(ctx context.Context, opts SearchOptions) ([]SearchResult, error) {
	// 如果提供了意图提示，按意图路由
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

	// 无意图或无法路由时，搜索所有层
	return r.searchAll(ctx, opts)
}

// searchAll 并行搜索所有层
func (r *Router) searchAll(ctx context.Context, opts SearchOptions) ([]SearchResult, error) {
	var wg sync.WaitGroup
	var mu sync.Mutex
	results := []SearchResult{}
	errors := []error{}

	// 搜索文本层 (L1)
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

	// 搜索 AST 层 (L2)
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

	// 搜索符号层 (L3)
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

	// 如果所有层都失败，返回第一个错误
	if len(results) == 0 && len(errors) > 0 {
		return nil, errors[0]
	}

	return results, nil
}

// routeByIntent 根据意图提示推断最佳搜索策略
//
// 参数:
//   - intent: 意图描述字符串
//
// 返回: 策略字符串 ("text", "ast", "symbol", "")
func (r *Router) routeByIntent(intent string) string {
	intent = strings.ToLower(intent)

	// 文本搜索关键词
	textKeywords := []string{"todo", "fixme", "comment", "string", "text", "pattern", "word", "find text"}
	// AST 搜索关键词
	astKeywords := []string{"function", "struct", "class", "node", "ast", "syntax", "definition", "declare"}
	// 符号搜索关键词
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

// countLines 统计字符串的行数
func countLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}
