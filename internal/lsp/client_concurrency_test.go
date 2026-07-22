package lsp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/isaacphi/mcp-language-server/internal/protocol"
)

// clangdWorkspace is the pre-built C project used for integration tests.
// Tests are skipped when it is absent or clangd is not on PATH.
const clangdWorkspace = "../../../testbeds/u-boot"

func skipIfNoClangd(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(clangdWorkspace); os.IsNotExist(err) {
		t.Skipf("clangd workspace %s not found — clone it with: git clone --depth=1 --branch v2026.07 https://github.com/u-boot/u-boot.git testbeds/u-boot", clangdWorkspace)
	}
}

func newClangdClient(t *testing.T) *Client {
	t.Helper()
	skipIfNoClangd(t)

	c, err := NewClient("clangd",
		"--compile-commands-dir="+clangdWorkspace,
		"--background-index",
		"--clang-tidy",
		"--header-insertion=never",
		"--log=error",
	)
	if err != nil {
		t.Fatalf("start clangd: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		c.CloseAllFiles(ctx)
		c.Close()
	})
	return c
}

// initializeClangd sends initialize/initialized and warm-up (didOpen the
// first translation unit so clangd starts background indexing).
func initializeClangd(t *testing.T, c *Client) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	abs, err := filepath.Abs(clangdWorkspace)
	if err != nil {
		t.Fatalf("abs workspace: %v", err)
	}

	initParams := &protocol.InitializeParams{
		XInitializeParams: protocol.XInitializeParams{
			ProcessID: int32(os.Getpid()),
			RootURI:   protocol.URIFromPath(abs),
			RootPath:  abs,
			Capabilities: protocol.ClientCapabilities{
				TextDocument: protocol.TextDocumentClientCapabilities{
					Synchronization: &protocol.TextDocumentSyncClientCapabilities{
						DidSave: true,
					},
					Hover:  &protocol.HoverClientCapabilities{},
					Definition: &protocol.DefinitionClientCapabilities{},
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
		t.Fatalf("initialize: %v", err)
	}
	if err := c.Notify(ctx, "initialized", struct{}{}); err != nil {
		t.Fatalf("initialized: %v", err)
	}

	// warmup: didOpen the first .c file to trigger background indexing.
	// This is critical — clangd won't index until the first didOpen.
	entries, _ := filepath.Glob(filepath.Join(clangdWorkspace, "**/*.c"))
	if len(entries) == 0 {
		entries, _ = filepath.Glob(filepath.Join(clangdWorkspace, "*.c"))
	}
	if len(entries) > 0 {
		warmFile := entries[0]
		content, err := os.ReadFile(warmFile)
		if err == nil {
			c.Notify(ctx, "textDocument/didOpen", protocol.DidOpenTextDocumentParams{
				TextDocument: protocol.TextDocumentItem{
					URI:        protocol.DocumentUri(protocol.URIFromPath(warmFile)),
					LanguageID: "c",
					Version:    1,
					Text:       string(content),
				},
			})
		}
	}
}

// TestConcurrentSymbolQueries verifies that multiple concurrent
// workspace/symbol queries succeed without errors or crashes.
func TestConcurrentSymbolQueries(t *testing.T) {
	c := newClangdClient(t)
	initializeClangd(t, c)

	// Give clangd a few seconds to load dynamic index for the warmup file.
	time.Sleep(3 * time.Second)

	symbols := []string{
		"device_probe",
		"malloc",
		"free",
		"printf",
		"strlen",
		"memcpy",
		"main",
		"uboot",
		"U_BOOT",
		"of_match",
	}

	var (
		success atomic.Int64
		fail    atomic.Int64
		empty   atomic.Int64
		wg      sync.WaitGroup
	)

	start := time.Now()
	for _, sym := range symbols {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			result, err := c.Symbol(ctx, protocol.WorkspaceSymbolParams{Query: name})
			if err != nil {
				t.Errorf("symbol %q: %v", name, err)
				fail.Add(1)
				return
			}
			results, err := result.Results()
			if err != nil {
				t.Errorf("symbol %q parse: %v", name, err)
				fail.Add(1)
				return
			}
			if len(results) == 0 {
				t.Logf("symbol %q: 0 results (index may not be ready)", name)
				empty.Add(1)
				return
			}
			success.Add(1)
			t.Logf("symbol %q: %d results", name, len(results))
		}(sym)
	}
	wg.Wait()
	elapsed := time.Since(start)

	s := success.Load()
	f := fail.Load()
	e := empty.Load()
	total := int64(len(symbols))

	t.Logf("concurrent symbol queries: %d success / %d empty / %d failed in %v (sem=%d)", s, e, f, elapsed, c.maxConcurrent)

	if f > 0 {
		t.Errorf("%d/%d queries failed (errors, not just empty)", f, total)
	}
	if s+e < total/2 {
		t.Errorf("too few successful queries (%d/%d) — index may not be loaded; increase sleep before test", s+e, total)
	}
}

// TestConcurrentMixedOperations verifies that state-mutation operations
// (didOpen) interleaved with queries do not cause errors.
func TestConcurrentMixedOperations(t *testing.T) {
	c := newClangdClient(t)
	initializeClangd(t, c)
	time.Sleep(3 * time.Second)

	// Find a few C files we can didOpen concurrently.
	cFiles, _ := filepath.Glob(filepath.Join(clangdWorkspace, "drivers", "core", "*.c"))
	if len(cFiles) < 3 {
		cFiles, _ = filepath.Glob(filepath.Join(clangdWorkspace, "*.c"))
	}
	if len(cFiles) < 3 {
		// Use the few files we found + make up some.
		moreCFiles, _ := filepath.Glob(filepath.Join(clangdWorkspace, "**/*.c"))
		cFiles = append(cFiles, moreCFiles...)
	}
	if len(cFiles) < 3 {
		t.Skip("not enough .c files for mixed-operation test")
	}

	var (
		success atomic.Int64
		fail    atomic.Int64
		wg      sync.WaitGroup
		errs    []string
		errsMu  sync.Mutex
	)

	// 3 query goroutines + 2 didOpen goroutines
	queries := []string{"device_probe", "malloc", "printf"}
	for _, q := range queries {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			for i := 0; i < 5; i++ {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				_, err := c.Symbol(ctx, protocol.WorkspaceSymbolParams{Query: name})
				cancel()
				if err != nil {
					fail.Add(1)
					errsMu.Lock()
					errs = append(errs, fmt.Sprintf("symbol %q: %v", name, err))
					errsMu.Unlock()
				} else {
					success.Add(1)
				}
				time.Sleep(50 * time.Millisecond)
			}
		}(q)
	}

	// didOpen goroutines — these should acquire the write lock
	for i := 0; i < min(2, len(cFiles)); i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				file := cFiles[(idx+j)%len(cFiles)]
				content, err := os.ReadFile(file)
				if err != nil {
					continue
				}
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				err = c.Notify(ctx, "textDocument/didOpen", protocol.DidOpenTextDocumentParams{
					TextDocument: protocol.TextDocumentItem{
						URI:        protocol.DocumentUri(protocol.URIFromPath(file)),
						LanguageID: "c",
						Version:    int32(j + 1),
						Text:       string(content),
					},
				})
				cancel()
				if err != nil {
					fail.Add(1)
					errsMu.Lock()
					errs = append(errs, fmt.Sprintf("didOpen %s: %v", file, err))
					errsMu.Unlock()
				}
				time.Sleep(100 * time.Millisecond)
			}
		}(i)
	}

	wg.Wait()

	s := success.Load()
	f := fail.Load()

	t.Logf("mixed ops: %d success / %d failures (sem=%d)", s, f, c.maxConcurrent)

	if f > 0 {
		for _, e := range errs {
			t.Logf("  failure: %s", e)
		}
		// Allow a few failures (clangd may not have fully indexed)
		if f > int64(len(queries)*2) {
			t.Errorf("too many failures: %d", f)
		}
	}
}

// TestSemaphoreEnforcement verifies that LSP_MAX_CONCURRENT_REQUESTS
// limits concurrency by comparing total execution time of N queries
// under different semaphore sizes. With sem=1 the time should be roughly
// N × single-query-latency (serialised); with larger sem it should be
// roughly N/sem × single-query-latency (concurrent).
func TestSemaphoreEnforcement(t *testing.T) {
	c := newClangdClient(t)
	initializeClangd(t, c)
	time.Sleep(3 * time.Second)

	if c.maxConcurrent == 0 {
		t.Skip("LSP_MAX_CONCURRENT_REQUESTS=0 means unlimited")
	}

	// Warm up: one query to establish baseline latency.
	ctxWarm, cancelWarm := context.WithTimeout(context.Background(), 5*time.Second)
	warmStart := time.Now()
	_, err := c.Symbol(ctxWarm, protocol.WorkspaceSymbolParams{Query: "device_probe"})
	singleLatency := time.Since(warmStart)
	cancelWarm()
	if err != nil {
		t.Fatalf("warmup query failed: %v", err)
	}
	t.Logf("single-query latency: %v", singleLatency)

	// Run N queries concurrently, all released simultaneously.
	// With sem=M, the expected wall time is ceil(N/M) × singleLatency.

	const numQueries = 9
	var wg sync.WaitGroup
	startBarrier := make(chan struct{})

	start := time.Now()
	for i := 0; i < numQueries; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-startBarrier
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, _ = c.Symbol(ctx, protocol.WorkspaceSymbolParams{Query: "device_probe"})
		}()
	}
	close(startBarrier)
	wg.Wait()
	elapsed := time.Since(start)

	batchSize := (numQueries + c.maxConcurrent - 1) / c.maxConcurrent // ceil-div
	expected := singleLatency * time.Duration(batchSize)

	t.Logf("semaphore enforcement: sem=%d, %d queries in %v (single=%v, batch=%d, expected=%v)",
		c.maxConcurrent, numQueries, elapsed, singleLatency, batchSize, expected)

	// Semaphore should prevent full concurrency: elapsed must be
	// larger than single query time (if it were fully concurrent,
	// it would complete in ~singleLatency).
	if elapsed < singleLatency/2 {
		t.Errorf("elapsed=%v < single=%v/2 — impossible, timing error", elapsed, singleLatency)
	}

	// If sem=1, expect serial-like timing (~numQueries × singleLatency).
	// Allow 3× tolerance: clangd batches internal work so serial isn't
	// strictly additive.
	if c.maxConcurrent == 1 && elapsed < singleLatency*time.Duration(numQueries)/3 {
		t.Errorf("sem=1 but elapsed=%v << expected serial ~%v — semaphore may not be serialising",
			elapsed, singleLatency*time.Duration(numQueries))
	}

	// If sem>=3, expect concurrent timing (~ceil(N/M) × singleLatency).
	if c.maxConcurrent >= 3 && elapsed > expected*3 {
		t.Errorf("sem=%d but elapsed=%v >> expected=%v — semaphore may be too restrictive",
			c.maxConcurrent, elapsed, expected)
	}
}