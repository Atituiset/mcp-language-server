package main

import (
	"net"
	"os"
	"os/exec"
	"testing"
	"time"
)

func isolateSessionDir(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
}

func TestSessionWriteReadRoundtrip(t *testing.T) {
	isolateSessionDir(t)
	ws := t.TempDir()

	want := daemonSession{
		PID:       12345,
		Addr:      "http://127.0.0.1:54321/mcp",
		Workspace: ws,
		LSP:       "clangd",
		Args:      []string{"--background-index"},
		StartedAt: time.Now().Truncate(time.Second),
	}
	if err := writeSession(ws, want); err != nil {
		t.Fatalf("writeSession: %v", err)
	}

	got, err := readSession(ws)
	if err != nil {
		t.Fatalf("readSession: %v", err)
	}
	if got.PID != want.PID || got.Addr != want.Addr || got.Workspace != want.Workspace ||
		got.LSP != want.LSP || len(got.Args) != 1 || got.Args[0] != want.Args[0] {
		t.Errorf("roundtrip mismatch:\nwant %+v\ngot  %+v", want, got)
	}

	// Session files must not be world-readable (they reveal the endpoint).
	path, _ := sessionFilePath(ws)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("expected 0600 perms, got %o", info.Mode().Perm())
	}

	removeSession(ws)
	if _, err := readSession(ws); err == nil {
		t.Error("expected readSession to fail after removeSession")
	}
}

func TestSessionLiveChecks(t *testing.T) {
	isolateSessionDir(t)
	ws := t.TempDir()

	// Alive process + real listener -> live.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	live := daemonSession{PID: os.Getpid(), Addr: "http://" + lis.Addr().String() + "/mcp"}
	if !sessionLive(live) {
		t.Error("expected sessionLive=true for live pid + open port")
	}

	// Alive process but closed port -> not live.
	lis.Close()
	if sessionLive(live) {
		t.Error("expected sessionLive=false when the port is closed")
	}

	// Dead pid -> not live. Reap a short-lived process to get a definitely
	// dead PID.
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("spawn helper process: %v", err)
	}
	dead := daemonSession{PID: cmd.Process.Pid, Addr: live.Addr}
	if pidAlive(dead.PID) {
		t.Skip("pid unexpectedly still alive")
	}
	if sessionLive(dead) {
		t.Error("expected sessionLive=false for dead pid")
	}

	// Corrupt / missing session file -> readLiveSession false.
	if _, ok := readLiveSession(ws); ok {
		t.Error("expected readLiveSession=false with no session file")
	}
	path, _ := sessionFilePath(ws)
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := readLiveSession(ws); ok {
		t.Error("expected readLiveSession=false for corrupt session file")
	}
}

func TestValidateLoopbackAddr(t *testing.T) {
	valid := []string{"127.0.0.1:0", "127.0.0.1:8080", ":0", "localhost:9000", "[::1]:8080"}
	for _, addr := range valid {
		if err := validateLoopbackAddr(addr); err != nil {
			t.Errorf("%s should be accepted: %v", addr, err)
		}
	}
	invalid := []string{"0.0.0.0:8080", "192.168.1.10:8080", "example.com:80"}
	for _, addr := range invalid {
		if err := validateLoopbackAddr(addr); err == nil {
			t.Errorf("%s should be rejected", addr)
		}
	}
}

func TestWorkspaceKeyDistinctAndStable(t *testing.T) {
	a := workspaceKey("/repo/a")
	if a != workspaceKey("/repo/a") {
		t.Error("workspaceKey must be stable")
	}
	if a == workspaceKey("/repo/b") {
		t.Error("workspaceKey must differ across workspaces")
	}
	if len(a) != 12 {
		t.Errorf("expected 12-char key, got %q", a)
	}
}
