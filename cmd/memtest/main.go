package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/isaacphi/mcp-language-server/internal/lsp"
	"github.com/isaacphi/mcp-language-server/internal/protocol"
)

func main() {
	workspace := "/home/atituiset/testbeds/u-boot"
	if len(os.Args) > 1 {
		workspace = os.Args[1]
	}

	abs, err := filepath.Abs(workspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "abs: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("workspace: %s\n", abs)

	// Start clangd (cd to workspace so relative paths in compile_commands work)
	os.Chdir(abs)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	c, err := lsp.NewClient("clangd",
		"--background-index",
		"--header-insertion=never",
		"--log=error",
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "start clangd: %v\n", err)
		os.Exit(1)
	}
	defer c.Close()

	pid := c.Cmd.Process.Pid
	fmt.Printf("clangd pid: %d\n", pid)

	printMem := func(label string) {
		rss, vms := readProcMem(pid)
		fmt.Printf("  [%s] RSS=%s, VmSize=%s\n", label, rss, vms)
	}

	printMem("startup")

	// Initialize
	initParams := &protocol.InitializeParams{
		XInitializeParams: protocol.XInitializeParams{
			ProcessID: int32(os.Getpid()),
			RootURI:   protocol.URIFromPath(abs),
			RootPath:  abs,
			Capabilities: protocol.ClientCapabilities{
				TextDocument: protocol.TextDocumentClientCapabilities{
					Hover:       &protocol.HoverClientCapabilities{},
					Definition:  &protocol.DefinitionClientCapabilities{},
					References:  &protocol.ReferenceClientCapabilities{},
					CallHierarchy: &protocol.CallHierarchyClientCapabilities{},
				},
				Workspace: protocol.WorkspaceClientCapabilities{
					Symbol: &protocol.WorkspaceSymbolClientCapabilities{},
				},
			},
		},
		WorkspaceFoldersInitializeParams: protocol.WorkspaceFoldersInitializeParams{
			WorkspaceFolders: []protocol.WorkspaceFolder{{
				URI:  protocol.URI(protocol.URIFromPath(abs)),
				Name: abs,
			}},
		},
	}
	var initResult protocol.InitializeResult
	if err := c.Call(ctx, "initialize", initParams, &initResult); err != nil {
		fmt.Fprintf(os.Stderr, "initialize: %v\n", err)
		os.Exit(1)
	}
	c.Notify(ctx, "initialized", struct{}{})
	printMem("after initialize")

	// Warmup: didOpen the first .c file
	entries, _ := filepath.Glob(filepath.Join(workspace, "**/*.c"))
	if len(entries) == 0 {
		entries, _ = filepath.Glob(filepath.Join(workspace, "*.c"))
	}
	if len(entries) > 0 {
		content, _ := os.ReadFile(entries[0])
		c.Notify(ctx, "textDocument/didOpen", protocol.DidOpenTextDocumentParams{
			TextDocument: protocol.TextDocumentItem{
				URI:        protocol.DocumentUri(protocol.URIFromPath(entries[0])),
				LanguageID: "c",
				Version:    1,
				Text:       string(content),
			},
		})
	}
	printMem("after didOpen (warmup)")

	// Poll memory during indexing
	fmt.Println("\nWaiting for background index...")
	for i := 0; i < 30; i++ {
		time.Sleep(2 * time.Second)
		printMem(fmt.Sprintf("indexing +%ds", (i+1)*2))
	}

	fmt.Println("\n=== Done ===")
}

func readProcMem(pid int) (rss, vms string) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return "N/A", "N/A"
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			rss = strings.TrimSpace(strings.TrimPrefix(line, "VmRSS:"))
		}
		if strings.HasPrefix(line, "VmSize:") {
			vms = strings.TrimSpace(strings.TrimPrefix(line, "VmSize:"))
		}
	}
	return
}
