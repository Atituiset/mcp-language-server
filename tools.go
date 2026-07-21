package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/isaacphi/mcp-language-server/internal/tools"
	"github.com/isaacphi/mcp-language-server/internal/tools/router"
	"github.com/mark3labs/mcp-go/mcp"
)

func (s *mcpServer) registerTools() error {
	coreLogger.Debug("Registering MCP tools")

	debugTools := os.Getenv("MCP_LS_DEBUG_TOOLS") != ""
	// Edit tools (edit_file, rename_symbol) are registered only when
	// explicitly enabled: the primary use case is read-only code
	// inspection, and an LLM-triggered edit is an incident there.
	editsEnabled := os.Getenv("MCP_LS_ENABLE_EDITS") != ""

	if editsEnabled {
		applyTextEditTool := mcp.NewTool("edit_file",
			mcp.WithDescription("Use to apply precise line-range text replacements in a file. startLine/endLine are 1-indexed and inclusive; leave newText empty to delete lines."),
			mcp.WithArray("edits",
				mcp.Required(),
				mcp.Description("List of edits to apply"),
				mcp.Items(map[string]any{
					"type": "object",
					"properties": map[string]any{
						"startLine": map[string]any{
							"type":        "number",
							"description": "Start line to replace, inclusive, one-indexed",
						},
						"endLine": map[string]any{
							"type":        "number",
							"description": "End line to replace, inclusive, one-indexed",
						},
						"newText": map[string]any{
							"type":        "string",
							"description": "Replacement text. Replace with the new text. Leave blank to remove lines.",
						},
					},
					"required": []string{"startLine", "endLine"},
				}),
			),
			mcp.WithString("filePath",
				mcp.Required(),
				mcp.Description("Path to the file to edit"),
			),
		)

		s.mcpServer.AddTool(applyTextEditTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := request.GetArguments()
			// Extract arguments
			filePath, ok := args["filePath"].(string)
			if !ok {
				return mcp.NewToolResultError("filePath must be a string"), nil
			}

			// Extract edits array
			editsArg, ok := args["edits"]
			if !ok {
				return mcp.NewToolResultError("edits is required"), nil
			}

			// Type assert and convert the edits
			editsArray, ok := editsArg.([]any)
			if !ok {
				return mcp.NewToolResultError("edits must be an array"), nil
			}

			var edits []tools.TextEdit
			for _, editItem := range editsArray {
				editMap, ok := editItem.(map[string]any)
				if !ok {
					return mcp.NewToolResultError("each edit must be an object"), nil
				}

				startLine, ok := editMap["startLine"].(float64)
				if !ok {
					return mcp.NewToolResultError("startLine must be a number"), nil
				}

				endLine, ok := editMap["endLine"].(float64)
				if !ok {
					return mcp.NewToolResultError("endLine must be a number"), nil
				}

				newText, _ := editMap["newText"].(string) // newText can be empty

				edits = append(edits, tools.TextEdit{
					StartLine: int(startLine),
					EndLine:   int(endLine),
					NewText:   newText,
				})
			}

			coreLogger.Debug("Executing edit_file for file: %s", filePath)
			response, err := tools.ApplyTextEdits(s.ctx, s.lspClient, filePath, edits)
			if err != nil {
				coreLogger.Error("Failed to apply edits: %v", err)
				return mcp.NewToolResultError(fmt.Sprintf("failed to apply edits: %v", err)), nil
			}
			return mcp.NewToolResultText(response), nil
		})
	}

	readDefinitionTool := mcp.NewTool("definition",
		mcp.WithDescription("Use when you already know a symbol's name and need its complete implementation source code. Do NOT use for exploratory or fuzzy search — use search first."),
		mcp.WithString("symbolName",
			mcp.Required(),
			mcp.Description("The name of the symbol whose definition you want to find (e.g. 'mypackage.MyFunction', 'MyType.MyMethod')"),
		),
	)

	s.mcpServer.AddTool(readDefinitionTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := request.GetArguments()
		// Extract arguments
		symbolName, ok := args["symbolName"].(string)
		if !ok {
			return mcp.NewToolResultError("symbolName must be a string"), nil
		}

		coreLogger.Debug("Executing definition for symbol: %s", symbolName)
		text, err := tools.ReadDefinition(s.ctx, s.lspClient, symbolName)
		if err != nil {
			coreLogger.Error("Failed to get definition: %v", err)
			return mcp.NewToolResultError(fmt.Sprintf("failed to get definition: %v", err)), nil
		}
		return mcp.NewToolResultText(text), nil
	})

	findReferencesTool := mcp.NewTool("references",
		mcp.WithDescription("Use when you know a symbol's name and need every location that references it across the codebase. Do NOT use for keyword or pattern search — use search."),
		mcp.WithString("symbolName",
			mcp.Required(),
			mcp.Description("The name of the symbol to search for (e.g. 'mypackage.MyFunction', 'MyType')"),
		),
	)

	s.mcpServer.AddTool(findReferencesTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := request.GetArguments()
		// Extract arguments
		symbolName, ok := args["symbolName"].(string)
		if !ok {
			return mcp.NewToolResultError("symbolName must be a string"), nil
		}

		coreLogger.Debug("Executing references for symbol: %s", symbolName)
		text, err := tools.FindReferences(s.ctx, s.lspClient, symbolName)
		if err != nil {
			coreLogger.Error("Failed to find references: %v", err)
			return mcp.NewToolResultError(fmt.Sprintf("failed to find references: %v", err)), nil
		}
		return mcp.NewToolResultText(text), nil
	})

	getDiagnosticsTool := mcp.NewTool("diagnostics",
		mcp.WithDescription("Use to get compiler errors and warnings the language server reports for a file — e.g. spotting known issues in code under review, or checking a file after an edit. Requires an exact filePath."),
		mcp.WithString("filePath",
			mcp.Required(),
			mcp.Description("The path to the file to get diagnostics for"),
		),
		mcp.WithNumber("contextLines",
			mcp.Description("Number of context lines to include around each diagnostic."),
		),
		mcp.WithBoolean("showLineNumbers",
			mcp.Description("If true, adds line numbers to the output"),
			mcp.DefaultBool(true),
		),
	)
	getDiagnosticsTool.Meta = appToolMeta("ui://diagnostics/dashboard")

	s.mcpServer.AddTool(getDiagnosticsTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := request.GetArguments()
		// Extract arguments
		filePath, ok := args["filePath"].(string)
		if !ok {
			return mcp.NewToolResultError("filePath must be a string"), nil
		}

		contextLines := 5 // default value
		switch v := args["contextLines"].(type) {
		case float64:
			contextLines = int(v)
		case int:
			contextLines = v
		}

		showLineNumbers := true // default value
		if showLineNumbersArg, ok := args["showLineNumbers"].(bool); ok {
			showLineNumbers = showLineNumbersArg
		}

		coreLogger.Debug("Executing diagnostics for file: %s", filePath)
		data, err := tools.GetDiagnosticsDataForFile(s.ctx, s.lspClient, filePath, contextLines, showLineNumbers)
		if err != nil {
			coreLogger.Error("Failed to get diagnostics: %v", err)
			return mcp.NewToolResultError(fmt.Sprintf("failed to get diagnostics: %v", err)), nil
		}
		text := tools.FormatDiagnosticsData(data, showLineNumbers)
		return newAppToolResult(data, text, "ui://diagnostics/dashboard"), nil
	})

	hoverTool := mcp.NewTool("hover",
		mcp.WithDescription("Use when you need the type signature or documentation of the symbol at a known file:line:column position. Do NOT use when you only know a name — use definition or search."),
		mcp.WithString("filePath",
			mcp.Required(),
			mcp.Description("The path to the file to get hover information for"),
		),
		mcp.WithNumber("line",
			mcp.Required(),
			mcp.Description("The line number where the hover is requested (1-indexed)"),
		),
		mcp.WithNumber("column",
			mcp.Required(),
			mcp.Description("The column number where the hover is requested (1-indexed)"),
		),
	)

	s.mcpServer.AddTool(hoverTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := request.GetArguments()
		// Extract arguments
		filePath, ok := args["filePath"].(string)
		if !ok {
			return mcp.NewToolResultError("filePath must be a string"), nil
		}

		// Handle both float64 and int for line and column due to JSON parsing
		var line, column int
		switch v := args["line"].(type) {
		case float64:
			line = int(v)
		case int:
			line = v
		default:
			return mcp.NewToolResultError("line must be a number"), nil
		}

		switch v := args["column"].(type) {
		case float64:
			column = int(v)
		case int:
			column = v
		default:
			return mcp.NewToolResultError("column must be a number"), nil
		}

		coreLogger.Debug("Executing hover for file: %s line: %d column: %d", filePath, line, column)
		text, err := tools.GetHoverInfo(s.ctx, s.lspClient, filePath, line, column)
		if err != nil {
			coreLogger.Error("Failed to get hover information: %v", err)
			return mcp.NewToolResultError(fmt.Sprintf("failed to get hover information: %v", err)), nil
		}
		return mcp.NewToolResultText(text), nil
	})

	if editsEnabled {
		renameSymbolTool := mcp.NewTool("rename_symbol",
			mcp.WithDescription("Use to rename a symbol at a known file:line:column position and update all references codebase-wide. Requires the exact position of the symbol."),
			mcp.WithString("filePath",
				mcp.Required(),
				mcp.Description("The path to the file containing the symbol to rename"),
			),
			mcp.WithNumber("line",
				mcp.Required(),
				mcp.Description("The line number where the symbol is located (1-indexed)"),
			),
			mcp.WithNumber("column",
				mcp.Required(),
				mcp.Description("The column number where the symbol is located (1-indexed)"),
			),
			mcp.WithString("newName",
				mcp.Required(),
				mcp.Description("The new name for the symbol"),
			),
		)

		s.mcpServer.AddTool(renameSymbolTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := request.GetArguments()
			// Extract arguments
			filePath, ok := args["filePath"].(string)
			if !ok {
				return mcp.NewToolResultError("filePath must be a string"), nil
			}

			newName, ok := args["newName"].(string)
			if !ok {
				return mcp.NewToolResultError("newName must be a string"), nil
			}

			// Handle both float64 and int for line and column due to JSON parsing
			var line, column int
			switch v := args["line"].(type) {
			case float64:
				line = int(v)
			case int:
				line = v
			default:
				return mcp.NewToolResultError("line must be a number"), nil
			}

			switch v := args["column"].(type) {
			case float64:
				column = int(v)
			case int:
				column = v
			default:
				return mcp.NewToolResultError("column must be a number"), nil
			}

			coreLogger.Debug("Executing rename_symbol for file: %s line: %d column: %d newName: %s", filePath, line, column, newName)
			text, err := tools.RenameSymbol(s.ctx, s.lspClient, filePath, line, column, newName)
			if err != nil {
				coreLogger.Error("Failed to rename symbol: %v", err)
				return mcp.NewToolResultError(fmt.Sprintf("failed to rename symbol: %v", err)), nil
			}
			return mcp.NewToolResultText(text), nil
		})
	}

	if debugTools {
		ripgrepTool := mcp.NewTool("ripgrep",
			mcp.WithDescription("Search for text patterns in files using ripgrep. Faster than LSP-based search but does not understand language semantics."),
			mcp.WithString("pattern",
				mcp.Required(),
				mcp.Description("The regex pattern to search for"),
			),
			mcp.WithBoolean("caseSensitive",
				mcp.Description("Case sensitive search (default: false)"),
			),
			mcp.WithBoolean("wholeWord",
				mcp.Description("Match whole words only (default: false)"),
			),
			mcp.WithNumber("maxCount",
				mcp.Description("Maximum number of matches per file (default: 100)"),
			),
			mcp.WithNumber("contextLines",
				mcp.Description("Number of context lines around matches (default: 0)"),
			),
			mcp.WithString("fileType",
				mcp.Description("Filter by file type (e.g., go, py, js, ts)"),
			),
			mcp.WithString("include",
				mcp.Description("Glob pattern to include files (e.g., *.go)"),
			),
		)

		s.mcpServer.AddTool(ripgrepTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := request.GetArguments()
			pattern, ok := args["pattern"].(string)
			if !ok {
				return mcp.NewToolResultError("pattern must be a string"), nil
			}

			opts := tools.RipgrepOptions{
				MaxCount: 100,
			}

			if v, ok := args["caseSensitive"].(bool); ok {
				opts.CaseSensitive = v
			}
			if v, ok := args["wholeWord"].(bool); ok {
				opts.WholeWord = v
			}
			if v, ok := args["maxCount"].(float64); ok {
				opts.MaxCount = int(v)
			}
			if v, ok := args["contextLines"].(float64); ok {
				opts.ContextLines = int(v)
			}
			if v, ok := args["fileType"].(string); ok {
				opts.FileType = v
			}
			if v, ok := args["include"].(string); ok {
				opts.Include = v
			}

			coreLogger.Debug("Executing ripgrep for pattern: %s", pattern)
			text, err := tools.SearchCode(s.ctx, s.config.workspaceDir, pattern, opts)
			if err != nil {
				coreLogger.Error("Failed to search: %v", err)
				return mcp.NewToolResultError(fmt.Sprintf("failed to search: %v", err)), nil
			}
			return mcp.NewToolResultText(text), nil
		})

		treesitterQueryTool := mcp.NewTool("treesitter_query",
			mcp.WithDescription("Query the AST using tree-sitter CSP patterns. Use this for structural pattern matching like finding all function definitions, specific AST node types, or complex tree patterns."),
			mcp.WithString("query",
				mcp.Required(),
				mcp.Description("CSP query pattern (e.g., '(function_definition) @func' to find all functions)"),
			),
			mcp.WithString("filePath",
				mcp.Description("Path to a specific file to query. If not provided, queries all C/C++ files in the workspace"),
			),
			mcp.WithString("language",
				mcp.Description("Language: 'c' or 'cpp' (auto-detected from file extension if filePath provided)"),
			),
		)

		s.mcpServer.AddTool(treesitterQueryTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := request.GetArguments()
			query, ok := args["query"].(string)
			if !ok {
				return mcp.NewToolResultError("query must be a string"), nil
			}

			var filePath, language string
			if v, ok := args["filePath"].(string); ok {
				filePath = v
			}
			if v, ok := args["language"].(string); ok {
				language = v
			}

			coreLogger.Debug("Executing treesitter_query: %s", query)
			text, err := tools.RunTreesitterQuery(s.ctx, s.config.workspaceDir, query, filePath, language)
			if err != nil {
				coreLogger.Error("Failed to run tree-sitter query: %v", err)
				return mcp.NewToolResultError(fmt.Sprintf("failed to run query: %v", err)), nil
			}
			return mcp.NewToolResultText(text), nil
		})

		treesitterASTTool := mcp.NewTool("treesitter_ast",
			mcp.WithDescription("Get the AST (Abstract Syntax Tree) structure of a C/C++ file. Useful for exploring the syntactic structure and finding specific node types."),
			mcp.WithString("filePath",
				mcp.Required(),
				mcp.Description("Path to the file to analyze"),
			),
			mcp.WithString("nodeType",
				mcp.Description("Filter to only show nodes of this type (e.g., 'function_definition', 'struct_specifier')"),
			),
			mcp.WithNumber("maxDepth",
				mcp.Description("Maximum tree depth to traverse (default: 10)"),
			),
		)

		s.mcpServer.AddTool(treesitterASTTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := request.GetArguments()
			filePath, ok := args["filePath"].(string)
			if !ok {
				return mcp.NewToolResultError("filePath must be a string"), nil
			}

			var nodeType string
			if v, ok := args["nodeType"].(string); ok {
				nodeType = v
			}

			maxDepth := 10
			if v, ok := args["maxDepth"].(float64); ok {
				maxDepth = int(v)
			}

			coreLogger.Debug("Executing treesitter_ast for file: %s", filePath)
			text, err := tools.GetAST(s.ctx, filePath, nodeType, maxDepth)
			if err != nil {
				coreLogger.Error("Failed to get AST: %v", err)
				return mcp.NewToolResultError(fmt.Sprintf("failed to get AST: %v", err)), nil
			}
			return mcp.NewToolResultText(text), nil
		})
	}

	// Unified search router
	searchRouter := s.searchRouter

	searchTool := mcp.NewTool("search",
		mcp.WithDescription("Primary search entry — use this FIRST whenever you need to find code, symbols, or text in the workspace. strategy='auto' (default) routes intelligently across text/AST/symbol layers; set 'text'/'ast'/'symbol' only to force a layer. Use filePath to narrow scope. Do NOT use this to read a known symbol's full implementation — use definition instead."),
		mcp.WithString("query",
			mcp.Required(),
			mcp.Description("What to search for"),
		),
		mcp.WithString("strategy",
			mcp.Description("Search strategy: 'auto' (intelligent routing), 'text' (ripgrep), 'ast' (tree-sitter), 'symbol' (LSP). Default: auto"),
			mcp.DefaultString("auto"),
		),
		mcp.WithString("intent",
			mcp.Description("Hint about what you're looking for: 'todo', 'function', 'struct', 'definition', 'reference', 'type', etc. Helps auto-routing."),
		),
		mcp.WithString("filePath",
			mcp.Description("Anchor the search on a file: text layer searches its include neighborhood (from compile_commands.json) or the file itself; AST layer limits to it"),
		),
		mcp.WithString("language",
			mcp.Description("Language for AST search: 'c' or 'cpp' (default: auto-detect)"),
		),
		mcp.WithNumber("maxCount",
			mcp.Description("Text strategy only: maximum matches per file (default: 100)"),
		),
		mcp.WithNumber("contextLines",
			mcp.Description("Text strategy only: context lines around matches (default: 0)"),
		),
		mcp.WithBoolean("caseSensitive",
			mcp.Description("Text strategy only: case sensitive search (default: false)"),
		),
		mcp.WithBoolean("wholeWord",
			mcp.Description("Text strategy only: match whole words only (default: false)"),
		),
	)

	s.mcpServer.AddTool(searchTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := request.GetArguments()
		query, ok := args["query"].(string)
		if !ok {
			return mcp.NewToolResultError("query must be a string"), nil
		}

		opts := router.SearchOptions{
			Query: query,
		}

		if v, ok := args["strategy"].(string); ok {
			opts.Strategy = v
		}
		if v, ok := args["intent"].(string); ok {
			opts.Intent = v
		}
		if v, ok := args["filePath"].(string); ok {
			opts.FilePath = v
		}
		if v, ok := args["language"].(string); ok {
			opts.Language = v
		}
		if v, ok := readIntArgument(args, "maxCount"); ok {
			opts.MaxCount = v
		}
		if v, ok := readIntArgument(args, "contextLines"); ok {
			opts.ContextLines = v
		}
		if v, ok := args["caseSensitive"].(bool); ok {
			opts.CaseSensitive = v
		}
		if v, ok := args["wholeWord"].(bool); ok {
			opts.WholeWord = v
		}

		coreLogger.Debug("Executing unified search: query=%s strategy=%s intent=%s", query, opts.Strategy, opts.Intent)
		results, err := searchRouter.Search(s.ctx, opts)
		if err != nil {
			coreLogger.Error("Search failed: %v", err)
			return mcp.NewToolResultError(fmt.Sprintf("search failed: %v", err)), nil
		}

		return formatSearchResults(results), nil
	})

	// Explicit layer tools (for code dispatch)
	if debugTools {
		searchTextTool := mcp.NewTool("search_text",
			mcp.WithDescription("Force L1 text search using ripgrep. Use for text patterns, TODO comments, strings."),
			mcp.WithString("query",
				mcp.Required(),
				mcp.Description("Text pattern or regex to search"),
			),
			mcp.WithString("filePath",
				mcp.Description("Limit to specific file"),
			),
		)

		s.mcpServer.AddTool(searchTextTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := request.GetArguments()
			query, ok := args["query"].(string)
			if !ok {
				return mcp.NewToolResultError("query must be a string"), nil
			}

			opts := router.SearchOptions{Query: query, Strategy: "text"}
			if v, ok := args["filePath"].(string); ok {
				opts.FilePath = v
			}

			results, err := searchRouter.Search(ctx, opts)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("search failed: %v", err)), nil
			}
			return formatSearchResults(results), nil
		})

		searchASTTool := mcp.NewTool("search_ast",
			mcp.WithDescription("Force L2 AST search using tree-sitter. Use for structural patterns, function definitions, AST node types."),
			mcp.WithString("query",
				mcp.Required(),
				mcp.Description("CSP query pattern (e.g., '(function_definition) @func')"),
			),
			mcp.WithString("filePath",
				mcp.Description("Limit to specific file"),
			),
			mcp.WithString("language",
				mcp.Description("Language: 'c' or 'cpp'"),
			),
		)

		s.mcpServer.AddTool(searchASTTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := request.GetArguments()
			query, ok := args["query"].(string)
			if !ok {
				return mcp.NewToolResultError("query must be a string"), nil
			}

			opts := router.SearchOptions{Query: query, Strategy: "ast"}
			if v, ok := args["filePath"].(string); ok {
				opts.FilePath = v
			}
			if v, ok := args["language"].(string); ok {
				opts.Language = v
			}

			results, err := searchRouter.Search(ctx, opts)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("search failed: %v", err)), nil
			}
			return formatSearchResults(results), nil
		})

		searchSymbolTool := mcp.NewTool("search_symbol",
			mcp.WithDescription("Force L3 symbol search using LSP. Use for symbol definitions, references, and semantic understanding."),
			mcp.WithString("query",
				mcp.Required(),
				mcp.Description("Symbol name to search"),
			),
			mcp.WithString("filePath",
				mcp.Description("Limit to specific file"),
			),
		)

		s.mcpServer.AddTool(searchSymbolTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := request.GetArguments()
			query, ok := args["query"].(string)
			if !ok {
				return mcp.NewToolResultError("query must be a string"), nil
			}

			opts := router.SearchOptions{Query: query, Strategy: "symbol"}
			if v, ok := args["filePath"].(string); ok {
				opts.FilePath = v
			}

			results, err := searchRouter.Search(ctx, opts)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("search failed: %v", err)), nil
			}
			return formatSearchResults(results), nil
		})
	}

	// Call hierarchy tools
	callersTool := mcp.NewTool("callers",
		mcp.WithDescription("Use when you need to know who calls a function (incoming call hierarchy), e.g. tracing untrusted data sources upward. Prefer filePath+line+column for precision. depth defaults to 1 and is clamped to 3 — do not request larger depths; iterate with follow-up calls instead."),
		mcp.WithString("symbolName",
			mcp.Description("Symbol name to resolve when filePath, line, and column are not provided"),
		),
		mcp.WithString("filePath",
			mcp.Description("Path to the file containing the function. Required unless symbolName is provided"),
		),
		mcp.WithNumber("line",
			mcp.Description("Line number where the function is located, 1-indexed. Required unless symbolName is provided"),
		),
		mcp.WithNumber("column",
			mcp.Description("Column number where the function is located, 1-indexed. Required unless symbolName is provided"),
		),
		mcp.WithNumber("depth",
			mcp.Description("Maximum depth to traverse (default: 1, max: 3)"),
		),
	)
	callersTool.Meta = appToolMeta("ui://call-hierarchy/graph")

	s.mcpServer.AddTool(callersTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := request.GetArguments()
		filePath, _ := args["filePath"].(string)
		symbolName, _ := args["symbolName"].(string)

		line, lineOK := readIntArgument(args, "line")
		column, columnOK := readIntArgument(args, "column")

		if (!lineOK || !columnOK || filePath == "") && symbolName != "" {
			loc, err := tools.ResolveSymbolLocation(s.ctx, s.lspClient, symbolName, filePath)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			filePath = loc.URI.Path()
			line = int(loc.Range.Start.Line + 1)
			column = int(loc.Range.Start.Character + 1)
			lineOK = true
			columnOK = true
		}

		if filePath == "" {
			return mcp.NewToolResultError("filePath is required unless symbolName resolves to a location"), nil
		}
		if !lineOK {
			return mcp.NewToolResultError("line must be a number"), nil
		}
		if !columnOK {
			return mcp.NewToolResultError("column must be a number"), nil
		}

		depth := 1
		if v, ok := readIntArgument(args, "depth"); ok {
			depth = v
			if depth > 3 {
				depth = 3
			}
			if depth < 1 {
				depth = 1
			}
		}

		coreLogger.Debug("Executing callers for %s:%d:%d depth=%d", filePath, line, column, depth)
		data, err := tools.GetCallersData(s.ctx, s.lspClient, filePath, line, column, depth)
		if err != nil {
			coreLogger.Error("Failed to get callers: %v", err)
			return mcp.NewToolResultError(fmt.Sprintf("failed to get callers: %v", err)), nil
		}
		text := tools.FormatCallHierarchyData(data)
		return newAppToolResult(data, text, "ui://call-hierarchy/graph"), nil
	})

	calleesTool := mcp.NewTool("callees",
		mcp.WithDescription("Use when you need to know what a function calls (outgoing call hierarchy), e.g. tracing sinks downward. Same parameters as callers; depth defaults to 1 and is clamped to 3."),
		mcp.WithString("symbolName",
			mcp.Description("Symbol name to resolve when filePath, line, and column are not provided"),
		),
		mcp.WithString("filePath",
			mcp.Description("Path to the file containing the function. Required unless symbolName is provided"),
		),
		mcp.WithNumber("line",
			mcp.Description("Line number where the function is located, 1-indexed. Required unless symbolName is provided"),
		),
		mcp.WithNumber("column",
			mcp.Description("Column number where the function is located, 1-indexed. Required unless symbolName is provided"),
		),
		mcp.WithNumber("depth",
			mcp.Description("Maximum depth to traverse (default: 1, max: 3)"),
		),
	)
	calleesTool.Meta = appToolMeta("ui://call-hierarchy/graph")

	s.mcpServer.AddTool(calleesTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := request.GetArguments()
		filePath, _ := args["filePath"].(string)
		symbolName, _ := args["symbolName"].(string)

		line, lineOK := readIntArgument(args, "line")
		column, columnOK := readIntArgument(args, "column")

		if (!lineOK || !columnOK || filePath == "") && symbolName != "" {
			loc, err := tools.ResolveSymbolLocation(s.ctx, s.lspClient, symbolName, filePath)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			filePath = loc.URI.Path()
			line = int(loc.Range.Start.Line + 1)
			column = int(loc.Range.Start.Character + 1)
			lineOK = true
			columnOK = true
		}

		if filePath == "" {
			return mcp.NewToolResultError("filePath is required unless symbolName resolves to a location"), nil
		}
		if !lineOK {
			return mcp.NewToolResultError("line must be a number"), nil
		}
		if !columnOK {
			return mcp.NewToolResultError("column must be a number"), nil
		}

		depth := 1
		if v, ok := readIntArgument(args, "depth"); ok {
			depth = v
			if depth > 3 {
				depth = 3
			}
			if depth < 1 {
				depth = 1
			}
		}

		coreLogger.Debug("Executing callees for %s:%d:%d depth=%d", filePath, line, column, depth)
		data, err := tools.GetCalleesData(s.ctx, s.lspClient, filePath, line, column, depth)
		if err != nil {
			coreLogger.Error("Failed to get callees: %v", err)
			return mcp.NewToolResultError(fmt.Sprintf("failed to get callees: %v", err)), nil
		}
		text := tools.FormatCallHierarchyData(data)
		return newAppToolResult(data, text, "ui://call-hierarchy/graph"), nil
	})

	// Struct usage tools
	if debugTools {
		findStructUsageTool := mcp.NewTool("find_struct_usage",
			mcp.WithDescription("Find all usages of a C/C++ struct type (variable declarations, function parameters, return types, etc.)."),
			mcp.WithString("structName",
				mcp.Required(),
				mcp.Description("Name of the struct to search for"),
			),
			mcp.WithString("filePath",
				mcp.Description("Limit search to a specific file"),
			),
			mcp.WithString("language",
				mcp.Description("Language: 'c' or 'cpp' (default: cpp)"),
			),
		)

		s.mcpServer.AddTool(findStructUsageTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := request.GetArguments()
			structName, ok := args["structName"].(string)
			if !ok {
				return mcp.NewToolResultError("structName must be a string"), nil
			}

			var filePath, language string
			if v, ok := args["filePath"].(string); ok {
				filePath = v
			}
			if v, ok := args["language"].(string); ok {
				language = v
			}

			coreLogger.Debug("Executing find_struct_usage for: %s", structName)
			text, err := tools.FindStructUsage(s.ctx, s.config.workspaceDir, structName, filePath, language)
			if err != nil {
				coreLogger.Error("Failed to find struct usage: %v", err)
				return mcp.NewToolResultError(fmt.Sprintf("failed to find struct usage: %v", err)), nil
			}
			return mcp.NewToolResultText(text), nil
		})

		findStructDefinitionTool := mcp.NewTool("find_struct_definition",
			mcp.WithDescription("Find the definition/declaration of a C/C++ struct type."),
			mcp.WithString("structName",
				mcp.Required(),
				mcp.Description("Name of the struct to find"),
			),
			mcp.WithString("filePath",
				mcp.Description("Limit search to a specific file"),
			),
			mcp.WithString("language",
				mcp.Description("Language: 'c' or 'cpp' (default: cpp)"),
			),
		)

		s.mcpServer.AddTool(findStructDefinitionTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := request.GetArguments()
			structName, ok := args["structName"].(string)
			if !ok {
				return mcp.NewToolResultError("structName must be a string"), nil
			}

			var filePath, language string
			if v, ok := args["filePath"].(string); ok {
				filePath = v
			}
			if v, ok := args["language"].(string); ok {
				language = v
			}

			coreLogger.Debug("Executing find_struct_definition for: %s", structName)
			text, err := tools.FindStructDefinition(s.ctx, s.config.workspaceDir, structName, filePath, language)
			if err != nil {
				coreLogger.Error("Failed to find struct definition: %v", err)
				return mcp.NewToolResultError(fmt.Sprintf("failed to find struct definition: %v", err)), nil
			}
			return mcp.NewToolResultText(text), nil
		})
	}

	if debugTools {
		coreLogger.Info("Debug tools enabled via MCP_LS_DEBUG_TOOLS")
	}
	if editsEnabled {
		coreLogger.Info("Edit tools enabled via MCP_LS_ENABLE_EDITS")
	}

	coreLogger.Info("Successfully registered all MCP tools")
	return nil
}

const maxTotalSearchBytes = 12 * 1024

func formatSearchResults(results []router.SearchResult) *mcp.CallToolResult {
	if len(results) == 0 {
		return mcp.NewToolResultText("No results found")
	}

	var b strings.Builder
	for _, r := range results {
		b.WriteString(fmt.Sprintf("=== [%s layer] (%d results) ===\n", r.Layer, r.Count))
		b.WriteString(r.Content)
		b.WriteString("\n\n")
	}

	out := b.String()
	if len(out) > maxTotalSearchBytes {
		out = out[:maxTotalSearchBytes]
		if idx := strings.LastIndex(out, "\n"); idx > 0 {
			out = out[:idx]
		}
		omitted := strings.Count(b.String(), "\n") - strings.Count(out, "\n")
		if omitted < 1 {
			omitted = 1
		}
		out += fmt.Sprintf("\n... [truncated, %d more lines, use filePath or strategy=text|ast|symbol to narrow]\n", omitted)
	}

	return mcp.NewToolResultText(out)
}

func appToolMeta(resourceURI string) *mcp.Meta {
	return mcp.NewMetaFromMap(map[string]any{
		"ui": map[string]any{
			"resourceUri": resourceURI,
		},
	})
}

func newAppToolResult(structured any, fallbackText, resourceURI string) *mcp.CallToolResult {
	result := mcp.NewToolResultStructured(structured, fallbackText)
	result.Meta = appToolMeta(resourceURI)
	return result
}

func readIntArgument(args map[string]any, name string) (int, bool) {
	switch v := args[name].(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	default:
		return 0, false
	}
}
