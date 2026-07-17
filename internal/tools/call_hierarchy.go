package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/isaacphi/mcp-language-server/internal/lsp"
	"github.com/isaacphi/mcp-language-server/internal/protocol"
)

// CallResult 调用结果结构体，包含函数名、文件路径、行号列号和深度
type CallResult struct {
	Name     string `json:"name"`     // 函数名称
	FilePath string `json:"filePath"` // 文件路径
	Line     int    `json:"line"`     // 行号
	Column   int    `json:"column"`   // 列号
	Depth    int    `json:"depth"`    // 调用的深度层级
}

type CallHierarchyData struct {
	Direction string       `json:"direction"`
	FilePath  string       `json:"filePath"`
	Line      int          `json:"line"`
	Column    int          `json:"column"`
	MaxDepth  int          `json:"maxDepth"`
	Prepared  bool         `json:"prepared"`
	Total     int          `json:"total"`
	Results   []CallResult `json:"results"`
}

// GetCallers 查找调用指定位置函数的函数（调用者）
//
// 参数:
//   - ctx: 上下文
//   - client: LSP 客户端
//   - filePath: 文件路径
//   - line: 行号（1-indexed）
//   - column: 列号（1-indexed）
//   - depth: 向上追溯的深度，默认为1，最大为10
//
// 返回: 格式化的调用者列表，按深度分组
func GetCallers(ctx context.Context, client *lsp.Client, filePath string, line, column, depth int) (string, error) {
	data, err := GetCallersData(ctx, client, filePath, line, column, depth)
	if err != nil {
		return "", err
	}
	if len(data.Results) == 0 {
		return FormatCallHierarchyData(data), nil
	}
	return FormatCallHierarchyData(data), nil
}

func GetCallersData(ctx context.Context, client *lsp.Client, filePath string, line, column, depth int) (CallHierarchyData, error) {
	if depth <= 0 {
		depth = 1
	}
	data := CallHierarchyData{
		Direction: "incoming",
		FilePath:  filePath,
		Line:      line,
		Column:    column,
		MaxDepth:  depth,
	}

	// 打开文件到 LSP 服务器
	if err := client.OpenFile(ctx, filePath); err != nil {
		return data, fmt.Errorf("failed to open file: %w", err)
	}

	// 在指定位置准备调用层级
	params := protocol.CallHierarchyPrepareParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: protocol.URIFromPath(filePath)},
			Position:     protocol.Position{Line: uint32(line - 1), Character: uint32(column - 1)},
		},
	}

	items, err := client.PrepareCallHierarchy(ctx, params)
	if err != nil {
		return data, fmt.Errorf("failed to prepare call hierarchy: %w", err)
	}

	if len(items) == 0 {
		return data, nil
	}
	data.Prepared = true

	// 收集初始位置的调用层级项
	var startItems []*protocol.CallHierarchyItem
	for i := range items {
		startItems = append(startItems, &items[i])
	}

	// 用于避免循环的已访问项集合
	seen := make(map[string]bool)
	var results []CallResult

	for _, item := range startItems {
		key := itemKey(item)
		seen[key] = true
	}

	// 递归收集调用者
	collectCallers(ctx, client, startItems, depth, 1, seen, &results)

	data.Results = results
	data.Total = len(results)
	return data, nil
}

// collectCallers 递归收集调用者
//
// 参数:
//   - ctx: 上下文
//   - client: LSP 客户端
//   - items: 当前层的调用层级项
//   - maxDepth: 最大深度
//   - currentDepth: 当前深度
//   - seen: 已访问项集合（用于避免循环）
//   - results: 结果收集器
func collectCallers(ctx context.Context, client *lsp.Client, items []*protocol.CallHierarchyItem, maxDepth, currentDepth int, seen map[string]bool, results *[]CallResult) {
	if currentDepth > maxDepth || len(items) == 0 {
		return
	}

	for _, item := range items {
		// 获取该函数的调用者（incoming calls）
		incomingParams := protocol.CallHierarchyIncomingCallsParams{Item: *item}
		incomingCalls, err := client.IncomingCalls(ctx, incomingParams)
		if err != nil {
			continue
		}

		for _, call := range incomingCalls {
			caller := call.From
			key := itemKey(&caller)
			if seen[key] {
				continue // 避免重复访问
			}
			seen[key] = true

			*results = append(*results, CallResult{
				Name:     caller.Name,
				FilePath: trimFileURI(string(caller.URI)),
				Line:     int(caller.Range.Start.Line + 1),
				Column:   int(caller.Range.Start.Character + 1),
				Depth:    currentDepth,
			})

			// 如果未达到最大深度，继续递归
			if currentDepth < maxDepth {
				collectCallers(ctx, client, []*protocol.CallHierarchyItem{&caller}, maxDepth, currentDepth+1, seen, results)
			}
		}
	}
}

// GetCallees 查找指定位置函数调用的函数（被调用者）
//
// 参数:
//   - ctx: 上下文
//   - client: LSP 客户端
//   - filePath: 文件路径
//   - line: 行号（1-indexed）
//   - column: 列号（1-indexed）
//   - depth: 向下追溯的深度，默认为1，最大为10
//
// 返回: 格式化的被调用者列表，按深度分组
func GetCallees(ctx context.Context, client *lsp.Client, filePath string, line, column, depth int) (string, error) {
	data, err := GetCalleesData(ctx, client, filePath, line, column, depth)
	if err != nil {
		return "", err
	}
	if len(data.Results) == 0 {
		return FormatCallHierarchyData(data), nil
	}
	return FormatCallHierarchyData(data), nil
}

func GetCalleesData(ctx context.Context, client *lsp.Client, filePath string, line, column, depth int) (CallHierarchyData, error) {
	if depth <= 0 {
		depth = 1
	}
	data := CallHierarchyData{
		Direction: "outgoing",
		FilePath:  filePath,
		Line:      line,
		Column:    column,
		MaxDepth:  depth,
	}

	// 打开文件到 LSP 服务器
	if err := client.OpenFile(ctx, filePath); err != nil {
		return data, fmt.Errorf("failed to open file: %w", err)
	}

	// 在指定位置准备调用层级
	params := protocol.CallHierarchyPrepareParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: protocol.URIFromPath(filePath)},
			Position:     protocol.Position{Line: uint32(line - 1), Character: uint32(column - 1)},
		},
	}

	items, err := client.PrepareCallHierarchy(ctx, params)
	if err != nil {
		return data, fmt.Errorf("failed to prepare call hierarchy: %w", err)
	}

	if len(items) == 0 {
		return data, nil
	}
	data.Prepared = true

	// 收集初始位置的调用层级项
	var startItems []*protocol.CallHierarchyItem
	for i := range items {
		startItems = append(startItems, &items[i])
	}

	// 用于避免循环的已访问项集合
	seen := make(map[string]bool)
	var results []CallResult

	for _, item := range startItems {
		key := itemKey(item)
		seen[key] = true
	}

	// 递归收集被调用者
	collectCallees(ctx, client, startItems, depth, 1, seen, &results)

	data.Results = results
	data.Total = len(results)
	return data, nil
}

// collectCallees 递归收集被调用者
//
// 参数:
//   - ctx: 上下文
//   - client: LSP 客户端
//   - items: 当前层的调用层级项
//   - maxDepth: 最大深度
//   - currentDepth: 当前深度
//   - seen: 已访问项集合（用于避免循环）
//   - results: 结果收集器
func collectCallees(ctx context.Context, client *lsp.Client, items []*protocol.CallHierarchyItem, maxDepth, currentDepth int, seen map[string]bool, results *[]CallResult) {
	if currentDepth > maxDepth || len(items) == 0 {
		return
	}

	for _, item := range items {
		// 获取该函数调用的函数（outgoing calls）
		outgoingParams := protocol.CallHierarchyOutgoingCallsParams{Item: *item}
		outgoingCalls, err := client.OutgoingCalls(ctx, outgoingParams)
		if err != nil {
			continue
		}

		for _, call := range outgoingCalls {
			callee := call.To
			key := itemKey(&callee)
			if seen[key] {
				continue // 避免重复访问
			}
			seen[key] = true

			*results = append(*results, CallResult{
				Name:     callee.Name,
				FilePath: trimFileURI(string(callee.URI)),
				Line:     int(callee.Range.Start.Line + 1),
				Column:   int(callee.Range.Start.Character + 1),
				Depth:    currentDepth,
			})

			// 如果未达到最大深度，继续递归
			if currentDepth < maxDepth {
				collectCallees(ctx, client, []*protocol.CallHierarchyItem{&callee}, maxDepth, currentDepth+1, seen, results)
			}
		}
	}
}

// itemKey 生成调用层级项的唯一键，用于去重和循环检测
func itemKey(item *protocol.CallHierarchyItem) string {
	return fmt.Sprintf("%s:%d:%d", string(item.URI), item.Range.Start.Line, item.Range.Start.Character)
}

// trimFileURI 移除 URI 的 file:// 前缀
func trimFileURI(uri string) string {
	documentURI, err := protocol.ParseDocumentUri(uri)
	if err != nil {
		return strings.TrimPrefix(uri, "file://")
	}
	return documentURI.Path()
}

// maxCallHierarchyBytes caps the formatted call hierarchy output to keep
// hot-function results (hundreds of callers at depth 3) bounded.
const maxCallHierarchyBytes = 16 * 1024

// formatCallResultsWithDepth 格式化调用结果，按深度分组输出
//
// 参数:
//   - results: 调用结果列表
//   - title: 标题（如 "Callers" 或 "Callees"）
//   - maxDepth: 最大深度
//
// 返回: 格式化的字符串；超过 maxCallHierarchyBytes 时截断并标注省略数量
func formatCallResultsWithDepth(results []CallResult, title string, maxDepth int) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("=== %s (depth 1-%d, %d total) ===\n\n", title, maxDepth, len(results)))

	// 按深度分组
	depthGroups := make(map[int][]CallResult)
	for _, r := range results {
		depthGroups[r.Depth] = append(depthGroups[r.Depth], r)
	}

	// 输出每个深度的结果
	omitted := 0
	for d := 1; d <= maxDepth; d++ {
		if group, ok := depthGroups[d]; ok {
			b.WriteString(fmt.Sprintf("--- Depth %d (%d functions) ---\n", d, len(group)))
			for _, r := range group {
				if b.Len() >= maxCallHierarchyBytes {
					omitted++
					continue
				}
				b.WriteString(fmt.Sprintf("  %s at %s:L%d:C%d\n", r.Name, r.FilePath, r.Line, r.Column))
			}
			b.WriteString("\n")
		}
	}

	if omitted > 0 {
		b.WriteString(fmt.Sprintf("... [truncated, %d more results, use lower depth or a narrower starting function]\n", omitted))
	}

	return b.String()
}

func FormatCallHierarchyData(data CallHierarchyData) string {
	if len(data.Results) == 0 {
		if !data.Prepared {
			return "No call hierarchy items found at this position"
		}
		if data.Direction == "incoming" {
			return "No callers found"
		}
		return "No callees found"
	}
	title := "Callees"
	if data.Direction == "incoming" {
		title = "Callers"
	}
	return formatCallResultsWithDepth(data.Results, title, data.MaxDepth)
}
