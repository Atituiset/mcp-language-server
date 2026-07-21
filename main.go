package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/isaacphi/mcp-language-server/internal/logging"
	"github.com/isaacphi/mcp-language-server/internal/lsp"
	"github.com/isaacphi/mcp-language-server/internal/tools/router"
	"github.com/isaacphi/mcp-language-server/internal/watcher"
	"github.com/mark3labs/mcp-go/server"
)

// Create a logger for the core component
var coreLogger = logging.NewLogger(logging.Core)

type config struct {
	workspaceDir string
	lspCommand   string
	lspArgs      []string
}

type mcpServer struct {
	config           config
	lspClient        *lsp.Client
	mcpServer        *server.MCPServer
	ctx              context.Context
	cancelFunc       context.CancelFunc
	workspaceWatcher *watcher.WorkspaceWatcher
	searchRouter     *router.Router
	// serverOptions are appended to the NewMCPServer options (daemon mode
	// uses this to install session hooks).
	serverOptions []server.ServerOption
}

func newConfigFlagSet(name string, cfg *config) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.StringVar(&cfg.workspaceDir, "workspace", "", "Path to workspace directory")
	fs.StringVar(&cfg.lspCommand, "lsp", "", "LSP command to run (args should be passed after --)")
	return fs
}

func (c *config) validate() error {
	if c.workspaceDir == "" {
		return fmt.Errorf("workspace directory is required")
	}

	workspaceDir, err := filepath.Abs(c.workspaceDir)
	if err != nil {
		return fmt.Errorf("failed to get absolute path for workspace: %v", err)
	}
	c.workspaceDir = workspaceDir

	if _, err := os.Stat(c.workspaceDir); os.IsNotExist(err) {
		return fmt.Errorf("workspace directory does not exist: %s", c.workspaceDir)
	}

	if c.lspCommand == "" {
		return fmt.Errorf("LSP command is required")
	}

	if _, err := exec.LookPath(c.lspCommand); err != nil {
		return fmt.Errorf("LSP command not found: %s", c.lspCommand)
	}

	return nil
}

func parseConfig() (*config, error) {
	cfg := &config{}
	fs := newConfigFlagSet(os.Args[0], cfg)
	if err := fs.Parse(os.Args[1:]); err != nil {
		return nil, err
	}

	// Get remaining args after -- as LSP arguments
	cfg.lspArgs = fs.Args()

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

func newServer(config *config) (*mcpServer, error) {
	ctx, cancel := context.WithCancel(context.Background())
	return &mcpServer{
		config:     *config,
		ctx:        ctx,
		cancelFunc: cancel,
	}, nil
}

func (s *mcpServer) initializeLSP() error {
	if err := os.Chdir(s.config.workspaceDir); err != nil {
		return fmt.Errorf("failed to change to workspace directory: %v", err)
	}

	client, err := lsp.NewClient(s.config.lspCommand, s.config.lspArgs...)
	if err != nil {
		return fmt.Errorf("failed to create LSP client: %v", err)
	}
	s.lspClient = client
	s.workspaceWatcher = watcher.NewWorkspaceWatcher(client)

	initResult, err := client.InitializeLSPClient(s.ctx, s.config.workspaceDir)
	if err != nil {
		return fmt.Errorf("initialize failed: %v", err)
	}

	coreLogger.Debug("Server capabilities: %+v", initResult.Capabilities)

	go s.workspaceWatcher.WatchWorkspace(s.ctx, s.config.workspaceDir)
	return client.WaitForServerReady(s.ctx)
}

// warmUpLSP nudges language servers that only start background indexing
// after the first textDocument/didOpen (e.g. clangd, see
// docs/benchmark-2026-07-17.md). It opens the first translation unit from
// compile_commands.json when one exists; otherwise it is a no-op.
func (s *mcpServer) warmUpLSP() {
	data, err := os.ReadFile(filepath.Join(s.config.workspaceDir, "compile_commands.json"))
	if err != nil {
		return
	}
	var entries []struct {
		File      string `json:"file"`
		Directory string `json:"directory"`
	}
	if err := json.Unmarshal(data, &entries); err != nil || len(entries) == 0 {
		return
	}

	file := entries[0].File
	if !filepath.IsAbs(file) && entries[0].Directory != "" {
		file = filepath.Join(entries[0].Directory, file)
	}

	ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
	defer cancel()
	if err := s.lspClient.OpenFile(ctx, file); err != nil {
		coreLogger.Debug("LSP warmup: failed to open %s: %v", file, err)
		return
	}
	coreLogger.Info("LSP warmup: opened %s to trigger background indexing", file)
}

// setup performs all initialization shared by the stdio and daemon modes:
// LSP startup, warmup, search router wiring, and MCP tool registration.
// Only the transport differs (ServeStdio vs HTTP).
func (s *mcpServer) setup() error {
	if err := s.initializeLSP(); err != nil {
		return err
	}

	s.warmUpLSP()

	var cacheTTL []int64
	if v, err := strconv.Atoi(os.Getenv("MCP_LS_CACHE_TTL")); err == nil && v > 0 {
		cacheTTL = append(cacheTTL, int64(v))
		coreLogger.Info("Using search cache TTL: %ds", v)
	}
	s.searchRouter = router.NewRouterWithClient(s.config.workspaceDir, s.lspClient, cacheTTL...)
	s.workspaceWatcher.OnFileChange = func(uri string) {
		s.searchRouter.InvalidateFile(uri)
		if strings.HasSuffix(uri, "compile_commands.json") {
			s.searchRouter.InvalidateIncludeMap()
		}
	}

	serverOpts := append([]server.ServerOption{
		server.WithLogging(),
		server.WithRecovery(),
		server.WithResourceCapabilities(false, true),
	}, s.serverOptions...)
	s.mcpServer = server.NewMCPServer(
		"MCP Language Server",
		"v0.4.0",
		serverOpts...,
	)

	s.registerUIResources()

	err := s.registerTools()
	if err != nil {
		return fmt.Errorf("tool registration failed: %v", err)
	}
	return nil
}

func (s *mcpServer) start() error {
	if err := s.setup(); err != nil {
		return err
	}

	return server.ServeStdio(s.mcpServer)
}

func main() {
	coreLogger.Info("MCP Language Server starting")

	// Subcommand dispatch: daemon/proxy change the deployment shape (one
	// shared LSP per workspace); no subcommand keeps the classic
	// one-process-per-client stdio mode.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "daemon":
			if err := runDaemon(os.Args[2:]); err != nil {
				coreLogger.Fatal("daemon: %v", err)
			}
			return
		case "proxy":
			if err := runProxy(os.Args[2:]); err != nil {
				coreLogger.Fatal("proxy: %v", err)
			}
			return
		}
	}

	done := make(chan struct{})
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	config, err := parseConfig()
	if err != nil {
		coreLogger.Fatal("%v", err)
	}

	server, err := newServer(config)
	if err != nil {
		coreLogger.Fatal("%v", err)
	}

	// Parent process monitoring channel
	parentDeath := make(chan struct{})

	// Monitor parent process termination
	// Claude desktop does not properly kill child processes for MCP servers
	go func() {
		ppid := os.Getppid()
		coreLogger.Debug("Monitoring parent process: %d", ppid)

		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				currentPpid := os.Getppid()
				if currentPpid != ppid && (currentPpid == 1 || ppid == 1) {
					coreLogger.Info("Parent process %d terminated (current ppid: %d), initiating shutdown", ppid, currentPpid)
					close(parentDeath)
					return
				}
			case <-done:
				return
			}
		}
	}()

	// Handle shutdown triggers
	go func() {
		select {
		case sig := <-sigChan:
			coreLogger.Info("Received signal %v in PID: %d", sig, os.Getpid())
			cleanup(server, done)
		case <-parentDeath:
			coreLogger.Info("Parent death detected, initiating shutdown")
			cleanup(server, done)
		}
	}()

	if err := server.start(); err != nil {
		coreLogger.Error("Server error: %v", err)
		cleanup(server, done)
		os.Exit(1)
	}

	<-done
	coreLogger.Info("Server shutdown complete for PID: %d", os.Getpid())
	os.Exit(0)
}

func cleanup(s *mcpServer, done chan struct{}) {
	coreLogger.Info("Cleanup initiated for PID: %d", os.Getpid())

	// Create a context with timeout for shutdown operations
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if s.lspClient != nil {
		coreLogger.Info("Closing open files")
		s.lspClient.CloseAllFiles(ctx)

		// Create a shorter timeout context for the shutdown request
		shutdownCtx, shutdownCancel := context.WithTimeout(ctx, 500*time.Millisecond)
		defer shutdownCancel()

		// Run shutdown in a goroutine with timeout to avoid blocking if LSP doesn't respond
		shutdownDone := make(chan struct{})
		go func() {
			coreLogger.Info("Sending shutdown request")
			if err := s.lspClient.Shutdown(shutdownCtx); err != nil {
				coreLogger.Error("Shutdown request failed: %v", err)
			}
			close(shutdownDone)
		}()

		// Wait for shutdown with timeout
		select {
		case <-shutdownDone:
			coreLogger.Info("Shutdown request completed")
		case <-time.After(1 * time.Second):
			coreLogger.Warn("Shutdown request timed out, proceeding with exit")
		}

		coreLogger.Info("Sending exit notification")
		if err := s.lspClient.Exit(ctx); err != nil {
			coreLogger.Error("Exit notification failed: %v", err)
		}

		coreLogger.Info("Closing LSP client")
		if err := s.lspClient.Close(); err != nil {
			coreLogger.Error("Failed to close LSP client: %v", err)
		}
	}

	// Send signal to the done channel
	select {
	case <-done: // Channel already closed
	default:
		close(done)
	}

	coreLogger.Info("Cleanup completed for PID: %d", os.Getpid())
}
