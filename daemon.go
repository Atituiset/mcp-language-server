package main

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/mark3labs/mcp-go/server"
)

// daemonSession is the discovery record a proxy uses to find the daemon
// owning a workspace's LSP instance.
type daemonSession struct {
	PID       int       `json:"pid"`
	Addr      string    `json:"addr"` // streamable HTTP endpoint, e.g. http://127.0.0.1:54321/mcp
	Workspace string    `json:"workspace"`
	LSP       string    `json:"lsp"`
	Args      []string  `json:"args,omitempty"`
	StartedAt time.Time `json:"startedAt"`
}

func sessionDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("cannot locate user cache dir: %w", err)
	}
	dir := filepath.Join(base, "mcp-language-server")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

func workspaceKey(workspace string) string {
	sum := sha1.Sum([]byte(workspace))
	return hex.EncodeToString(sum[:])[:12]
}

func sessionFilePath(workspace string) (string, error) {
	dir, err := sessionDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "daemon-"+workspaceKey(workspace)+".json"), nil
}

func sessionLockPath(workspace string) (string, error) {
	dir, err := sessionDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "daemon-"+workspaceKey(workspace)+".lock"), nil
}

func sessionLogPath(workspace string) (string, error) {
	dir, err := sessionDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "daemon-"+workspaceKey(workspace)+".log"), nil
}

func writeSession(workspace string, sess daemonSession) error {
	path, err := sessionFilePath(workspace)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(sess, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func removeSession(workspace string) {
	if path, err := sessionFilePath(workspace); err == nil {
		os.Remove(path)
	}
}

func readSession(workspace string) (daemonSession, error) {
	var sess daemonSession
	path, err := sessionFilePath(workspace)
	if err != nil {
		return sess, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return sess, err
	}
	if err := json.Unmarshal(data, &sess); err != nil {
		return sess, err
	}
	return sess, nil
}

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// sessionLive verifies both that the recorded process is alive and that its
// HTTP endpoint accepts connections (guards against PID reuse).
func sessionLive(sess daemonSession) bool {
	if !pidAlive(sess.PID) {
		return false
	}
	u, err := url.Parse(sess.Addr)
	if err != nil || u.Host == "" {
		return false
	}
	conn, err := net.DialTimeout("tcp", u.Host, 500*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func readLiveSession(workspace string) (daemonSession, bool) {
	sess, err := readSession(workspace)
	if err != nil || !sessionLive(sess) {
		return daemonSession{}, false
	}
	return sess, true
}

// validateLoopbackAddr rejects non-loopback bind addresses: the HTTP
// endpoint is unauthenticated, so the loopback interface is the trust
// boundary.
func validateLoopbackAddr(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid --addr %q: %v", addr, err)
	}
	if host == "" || host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("--addr %q is not loopback; the MCP endpoint is unauthenticated and must bind to 127.0.0.1/::1", addr)
}

// runDaemon starts a long-lived MCP server for one workspace, serving
// streamable HTTP on a loopback address. Exactly one daemon should own a
// workspace; proxies discover it via the session file.
func runDaemon(argv []string) error {
	cfg := &config{}
	fs := newConfigFlagSet("daemon", cfg)
	addr := fs.String("addr", "127.0.0.1:0", "Listen address (loopback only; :0 picks a free port)")
	idleTimeout := fs.Duration("idle-timeout", 30*time.Minute, "Shut down after this long with no active client sessions")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	cfg.lspArgs = fs.Args()
	if err := cfg.validate(); err != nil {
		return err
	}
	if err := validateLoopbackAddr(*addr); err != nil {
		return err
	}

	if sess, ok := readLiveSession(cfg.workspaceDir); ok {
		return fmt.Errorf("daemon already running for %s at %s (pid %d)", cfg.workspaceDir, sess.Addr, sess.PID)
	}

	// Session counting for the idle reaper: registered/unregistered hooks
	// bracket each client session; the timestamp tracks when the count last
	// dropped to zero (initialized to now so a never-used daemon also exits).
	hooks := &server.Hooks{}
	var activeSessions atomic.Int64
	lastIdleSince := atomic.Int64{}
	lastIdleSince.Store(time.Now().UnixNano())
	hooks.AddOnRegisterSession(func(_ context.Context, _ server.ClientSession) {
		activeSessions.Add(1)
		lastIdleSince.Store(0)
	})
	hooks.AddOnUnregisterSession(func(_ context.Context, _ server.ClientSession) {
		if activeSessions.Add(-1) <= 0 {
			lastIdleSince.Store(time.Now().UnixNano())
		}
	})

	srv, err := newServer(cfg)
	if err != nil {
		return err
	}
	srv.serverOptions = append(srv.serverOptions, server.WithHooks(hooks))

	if err := srv.setup(); err != nil {
		return err
	}

	lis, err := net.Listen("tcp", *addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", *addr, err)
	}
	endpoint := (&url.URL{Scheme: "http", Host: lis.Addr().String(), Path: "/mcp"}).String()

	streamable := server.NewStreamableHTTPServer(srv.mcpServer)
	httpSrv := &http.Server{Handler: streamable}

	sess := daemonSession{
		PID:       os.Getpid(),
		Addr:      endpoint,
		Workspace: cfg.workspaceDir,
		LSP:       cfg.lspCommand,
		Args:      cfg.lspArgs,
		StartedAt: time.Now(),
	}
	if err := writeSession(cfg.workspaceDir, sess); err != nil {
		lis.Close()
		return fmt.Errorf("write session file: %w", err)
	}
	coreLogger.Info("daemon serving %s at %s (pid %d, idle-timeout %s)", cfg.workspaceDir, endpoint, os.Getpid(), *idleTimeout)

	done := make(chan struct{})
	var shutdownOnce sync.Once
	shutdown := func() {
		shutdownOnce.Do(func() {
			coreLogger.Info("daemon shutting down")
			removeSession(cfg.workspaceDir)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := httpSrv.Shutdown(ctx); err != nil {
				coreLogger.Error("HTTP shutdown: %v", err)
			}
			cleanup(srv, done)
		})
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigChan
		coreLogger.Info("daemon received signal %v", sig)
		shutdown()
	}()

	// Idle reaper: exit once the daemon has had no active client sessions
	// for idleTimeout, so an unused daemon (and its LSP server) cannot leak.
	go func() {
		tick := *idleTimeout / 4
		if tick > 30*time.Second {
			tick = 30 * time.Second
		}
		if tick <= 0 {
			tick = time.Second
		}
		for {
			select {
			case <-done:
				return
			case <-time.After(tick):
				if activeSessions.Load() != 0 {
					continue
				}
				if since := lastIdleSince.Load(); since > 0 && time.Since(time.Unix(0, since)) > *idleTimeout {
					coreLogger.Info("no client sessions for %s, shutting down", *idleTimeout)
					shutdown()
					return
				}
			}
		}
	}()

	serveErr := httpSrv.Serve(lis)
	if serveErr == http.ErrServerClosed {
		serveErr = nil
	}
	shutdown()
	<-done
	coreLogger.Info("daemon stopped")
	return serveErr
}
