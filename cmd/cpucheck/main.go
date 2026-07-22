package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/isaacphi/mcp-language-server/internal/lsp"
	"github.com/isaacphi/mcp-language-server/internal/protocol"
)

func main() {
	workspace := "/home/atituiset/Projects/testbeds/u-boot"
	if len(os.Args) > 1 {
		workspace = os.Args[1]
	}

	abs, _ := filepath.Abs(workspace)
	os.Chdir(abs)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	c, err := lsp.NewClient("clangd", "--background-index", "--header-insertion=never", "--log=error")
	if err != nil {
		fmt.Fprintf(os.Stderr, "start: %v\n", err)
		os.Exit(1)
	}
	defer c.Close()

	pid := c.Cmd.Process.Pid
	pids := fmt.Sprintf("%d", pid)

	// Measure helper: RSS + CPU%
	measure := func(label string) {
		rss := readProcField(pid, "VmRSS")
		cpu := processCPU(pids)
		fmt.Printf("  [%s] RSS=%s  CPU=%s%%\n", label, rss, cpu)
	}

	// Init
	measure("startup")
	initParams := &protocol.InitializeParams{
		XInitializeParams: protocol.XInitializeParams{
			ProcessID: int32(os.Getpid()),
			RootURI:   protocol.URIFromPath(abs),
			RootPath:  abs,
			Capabilities: protocol.ClientCapabilities{
				TextDocument: protocol.TextDocumentClientCapabilities{
					Hover: &protocol.HoverClientCapabilities{}, Definition: &protocol.DefinitionClientCapabilities{},
					References: &protocol.ReferenceClientCapabilities{},
				},
				Workspace: protocol.WorkspaceClientCapabilities{Symbol: &protocol.WorkspaceSymbolClientCapabilities{}},
			},
		},
		WorkspaceFoldersInitializeParams: protocol.WorkspaceFoldersInitializeParams{
			WorkspaceFolders: []protocol.WorkspaceFolder{{
				URI: protocol.URI(protocol.URIFromPath(abs)), Name: abs,
			}},
		},
	}
	var initResult protocol.InitializeResult
	c.Call(ctx, "initialize", initParams, &initResult)
	c.Notify(ctx, "initialized", struct{}{})
	measure("after init")

	// Warmup
	entries, _ := filepath.Glob(filepath.Join(workspace, "**/*.c"))
	if len(entries) > 0 {
		content, _ := os.ReadFile(entries[0])
		c.Notify(ctx, "textDocument/didOpen", protocol.DidOpenTextDocumentParams{
			TextDocument: protocol.TextDocumentItem{
				URI: protocol.DocumentUri(protocol.URIFromPath(entries[0])), LanguageID: "c", Version: 1, Text: string(content),
			},
		})
	}
	measure("after warmup")

	// Wait for index to settle
	fmt.Println("\nWaiting for index to settle (10s)...")
	for i := 0; i < 5; i++ {
		time.Sleep(2 * time.Second)
		measure(fmt.Sprintf("indexing +%ds", (i+1)*2))
	}

	// ---- Concurrent query stress ----
	fmt.Println("\n=== Concurrent query stress (10 goroutines, 3 rounds) ===")

	symbols := []string{"device_probe", "malloc", "printf", "uboot", "memcpy",
		"strlen", "free", "main", "U_BOOT", "of_match"}

	for round := 0; round < 3; round++ {
		var wg sync.WaitGroup
		var success, fail atomic.Int64
		start := time.Now()

		for _, sym := range symbols {
			wg.Add(1)
			go func(name string) {
				defer wg.Done()
				ctx2, cancel2 := context.WithTimeout(ctx, 10*time.Second)
				defer cancel2()
				result, err := c.Symbol(ctx2, protocol.WorkspaceSymbolParams{Query: name})
				if err != nil {
					fail.Add(1)
					return
				}
				results, _ := result.Results()
				if len(results) == 0 {
					fail.Add(1)
					return
				}
				success.Add(1)
			}(sym)
		}
		wg.Wait()

		elapsed := time.Since(start)
		cpu := processCPU(pids)
		rss := readProcField(pid, "VmRSS")
		fmt.Printf("  round %d: %d ok / %d fail / %v / CPU=%s%% / RSS=%s\n",
			round+1, success.Load(), fail.Load(), elapsed, cpu, rss)

		time.Sleep(500 * time.Millisecond)
	}

	// ---- Heavy query stress: references ----
	fmt.Println("\n=== Heavy query: references(\"device_probe\") x5 ===")
	symResult, _ := c.Symbol(ctx, protocol.WorkspaceSymbolParams{Query: "device_probe"})
	symResults, _ := symResult.Results()
	if len(symResults) > 0 {
		loc := symResults[0].GetLocation()
		c.OpenFile(ctx, loc.URI.Path())

		for i := 0; i < 5; i++ {
			start := time.Now()
			_, err := c.References(ctx, protocol.ReferenceParams{
				TextDocumentPositionParams: protocol.TextDocumentPositionParams{
					TextDocument: protocol.TextDocumentIdentifier{URI: loc.URI},
					Position:     loc.Range.Start,
				},
				Context: protocol.ReferenceContext{IncludeDeclaration: false},
			})
			elapsed := time.Since(start)
			cpu := processCPU(pids)
			if err != nil {
				fmt.Printf("  ref %d: ERROR %v / %v / CPU=%s%%\n", i+1, err, elapsed, cpu)
			} else {
				fmt.Printf("  ref %d: OK / %v / CPU=%s%%\n", i+1, elapsed, cpu)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}

	// Idle CPU
	time.Sleep(2 * time.Second)
	cpu := processCPU(pids)
	fmt.Printf("\n=== Idle CPU: %s%% ===\n", cpu)
}

func readProcField(pid int, field string) string {
	data, _ := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, field+":") {
			return strings.TrimSpace(strings.TrimPrefix(line, field+":"))
		}
	}
	return "N/A"
}

// processCPU returns total CPU% for the given PIDs via `ps -p pid1,pid2 -o %cpu=`.
func processCPU(pids string) string {
	out, err := exec.Command("ps", "-p", pids, "-o", "%cpu=").Output()
	if err != nil {
		return "N/A"
	}
	total := 0.0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		v, _ := strconv.ParseFloat(strings.TrimSpace(line), 64)
		total += v
	}
	return fmt.Sprintf("%.0f", total)
}
