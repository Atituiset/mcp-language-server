package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// runProxy presents a plain stdio MCP server to its client while forwarding
// every call to the workspace's shared daemon (spawning one when absent).
// Proxies are disposable: they hold no state and exit on stdin EOF.
func runProxy(argv []string) error {
	cfg := &config{}
	fs := newConfigFlagSet("proxy", cfg)
	if err := fs.Parse(argv); err != nil {
		return err
	}
	cfg.lspArgs = fs.Args()
	if err := cfg.validate(); err != nil {
		return err
	}

	addr, err := ensureDaemon(cfg)
	if err != nil {
		return err
	}
	coreLogger.Info("proxy connecting to daemon at %s", addr)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c, err := client.NewStreamableHttpClient(addr)
	if err != nil {
		return err
	}
	defer c.Close()

	if err := c.Start(ctx); err != nil {
		return fmt.Errorf("connect daemon: %w", err)
	}

	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "mcp-language-server-proxy", Version: "0.4.0"}
	if _, err := c.Initialize(ctx, initReq); err != nil {
		return fmt.Errorf("initialize daemon session: %w", err)
	}

	srv := server.NewMCPServer(
		"MCP Language Server",
		"v0.4.0",
		server.WithRecovery(),
		server.WithResourceCapabilities(false, true),
	)

	if err := mirrorTools(ctx, c, srv); err != nil {
		return err
	}
	mirrorResources(ctx, c, srv)

	// Forward daemon-initiated notifications (e.g. logging) to the stdio client.
	c.OnNotification(func(n mcp.JSONRPCNotification) {
		srv.SendNotificationToAllClients(n.Method, n.Params.AdditionalFields)
	})

	// ServeStdio returns on stdin EOF; the proxy then exits without touching
	// the daemon, which other clients may still be using.
	return server.ServeStdio(srv)
}

// mirrorTools copies the daemon's tool surface onto the local stdio server
// with pass-through handlers, so clients see exactly the daemon's tools
// (schemas, descriptions, and Meta.ui resource links included).
func mirrorTools(ctx context.Context, c *client.Client, srv *server.MCPServer) error {
	toolsResult, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		return fmt.Errorf("mirror tools from daemon: %w", err)
	}
	for _, t := range toolsResult.Tools {
		tool := t
		srv.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			req := mcp.CallToolRequest{}
			req.Params.Name = tool.Name
			req.Params.Arguments = request.GetArguments()
			return c.CallTool(ctx, req)
		})
	}
	return nil
}

// mirrorResources copies the daemon's resources (e.g. MCP App UIs) with
// pass-through read handlers. Failure is non-fatal: tools still work.
func mirrorResources(ctx context.Context, c *client.Client, srv *server.MCPServer) {
	resResult, err := c.ListResources(ctx, mcp.ListResourcesRequest{})
	if err != nil {
		coreLogger.Debug("mirror resources from daemon: %v", err)
		return
	}
	for _, r := range resResult.Resources {
		res := r
		srv.AddResource(res, func(ctx context.Context, request mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
			req := mcp.ReadResourceRequest{}
			req.Params.URI = res.URI
			result, err := c.ReadResource(ctx, req)
			if err != nil {
				return nil, err
			}
			return result.Contents, nil
		})
	}
}

// ensureDaemon returns the HTTP endpoint of the workspace's daemon, spawning
// one when none is running. A flock on the lockfile serializes concurrent
// proxies so only one of them spawns a daemon.
func ensureDaemon(cfg *config) (string, error) {
	if sess, ok := readLiveSession(cfg.workspaceDir); ok {
		return sess.Addr, nil
	}

	lockPath, err := sessionLockPath(cfg.workspaceDir)
	if err != nil {
		return "", err
	}
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return "", err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return "", fmt.Errorf("acquire daemon lock: %w", err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	// Re-check inside the lock: another proxy may have spawned it already.
	if sess, ok := readLiveSession(cfg.workspaceDir); ok {
		return sess.Addr, nil
	}

	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate own executable: %w", err)
	}
	argv := []string{"daemon", "--workspace", cfg.workspaceDir, "--lsp", cfg.lspCommand}
	if len(cfg.lspArgs) > 0 {
		argv = append(argv, "--")
		argv = append(argv, cfg.lspArgs...)
	}

	logPath, err := sessionLogPath(cfg.workspaceDir)
	if err != nil {
		return "", err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return "", err
	}
	defer logFile.Close()

	cmd := exec.Command(exe, argv...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("spawn daemon: %w", err)
	}
	coreLogger.Info("spawned daemon (pid %d) for %s, logs at %s", cmd.Process.Pid, cfg.workspaceDir, logPath)

	// LSP cold-start indexing can take tens of seconds on large repos.
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if sess, ok := readLiveSession(cfg.workspaceDir); ok {
			return sess.Addr, nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return "", fmt.Errorf("daemon for %s did not come up within 60s (see %s)", cfg.workspaceDir, logPath)
}
