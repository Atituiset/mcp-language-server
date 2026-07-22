package lsp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/isaacphi/mcp-language-server/internal/protocol"
)

type Client struct {
	Cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	stderr io.ReadCloser

	// writeMu serializes stdin writes (header+body must stay atomic)
	writeMu sync.Mutex

	// dead is set when the read loop exits (process died or pipe closed)
	dead atomic.Bool

	// Request ID counter
	nextID atomic.Int32

	// Response handlers
	handlers   map[string]chan *Message
	handlersMu sync.RWMutex

	// Server request handlers
	serverRequestHandlers map[string]ServerRequestHandler
	serverHandlersMu      sync.RWMutex

	// Notification handlers
	notificationHandlers map[string]NotificationHandler
	notificationMu       sync.RWMutex

	// Diagnostic cache
	diagnostics   map[protocol.DocumentUri][]protocol.Diagnostic
	diagnosticsMu sync.RWMutex

	// Diagnostic waiters: callers of WaitForDiagnostics block until
	// publishDiagnostics arrives for the requested URI or timeout expires.
	diagWaiters   map[protocol.DocumentUri][]chan struct{}
	diagWaitersMu sync.Mutex

	// Text document sync kind reported by the server during initialization.
	// 0=None, 1=Full, 2=Incremental
	syncKind protocol.TextDocumentSyncKind

	// Files are currently opened by the LSP
	openFiles   map[string]*OpenFileInfo
	openFilesMu sync.RWMutex

	// stateMu protects LSP session consistency: queries (RLock) run
	// concurrently; state mutations (Lock) block all queries during
	// didOpen/didChange/didClose/didSave.
	stateMu sync.RWMutex

	// sem is an optional concurrency cap (nil = unlimited). Controlled by
	// the LSP_MAX_CONCURRENT_REQUESTS environment variable.
	sem chan struct{}

	// maxConcurrent records the semaphore capacity for logging.
	maxConcurrent int
}

func NewClient(command string, args ...string) (*Client, error) {
	cmd := exec.Command(command, args...)
	// Copy env
	cmd.Env = os.Environ()

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	// LSP_MAX_CONCURRENT_REQUESTS caps concurrent in-flight LSP requests
	// (0 = unlimited, 1 = fully serial, default 3).
	maxConc := 3
	if v := os.Getenv("LSP_MAX_CONCURRENT_REQUESTS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			maxConc = n
		}
	}
	var sem chan struct{}
	if maxConc > 0 {
		sem = make(chan struct{}, maxConc)
	}

	client := &Client{
		Cmd:                   cmd,
		stdin:                 stdin,
		stdout:                bufio.NewReader(stdout),
		stderr:                stderr,
		handlers:              make(map[string]chan *Message),
		notificationHandlers:  make(map[string]NotificationHandler),
		serverRequestHandlers: make(map[string]ServerRequestHandler),
		diagnostics:           make(map[protocol.DocumentUri][]protocol.Diagnostic),
		diagWaiters:           make(map[protocol.DocumentUri][]chan struct{}),
		openFiles:             make(map[string]*OpenFileInfo),
		sem:                   sem,
		maxConcurrent:         maxConc,
	}

	// Start the LSP server process
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start LSP server: %w", err)
	}

	// Handle stderr in a separate goroutine with proper logging
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Text()
			processLogger.Info("%s", line)
		}
		if err := scanner.Err(); err != nil {
			lspLogger.Error("Error reading LSP server stderr: %v", err)
		}
	}()

	// Start message handling loop
	go client.handleMessages()

	return client, nil
}

// Alive reports whether the LSP connection is still usable. It turns false
// once the read loop exits (server process died or closed the pipe).
func (c *Client) Alive() bool {
	return !c.dead.Load()
}

// markDead flags the connection as closed so subsequent Call/Notify fail fast
// instead of blocking on a response that will never arrive.
func (c *Client) markDead() {
	c.dead.Store(true)
}

// failPendingRequests unblocks every in-flight Call with an error, so tool
// handlers do not hang until their context times out after the server dies.
func (c *Client) failPendingRequests(cause error) {
	c.handlersMu.Lock()
	defer c.handlersMu.Unlock()
	for id, ch := range c.handlers {
		msg := &Message{
			JSONRPC: "2.0",
			Error: &ResponseError{
				Code:    -32001,
				Message: fmt.Sprintf("LSP connection closed: %v", cause),
			},
		}
		select {
		case ch <- msg:
		default:
		}
		close(ch)
		delete(c.handlers, id)
	}
}

func (c *Client) RegisterNotificationHandler(method string, handler NotificationHandler) {
	c.notificationMu.Lock()
	defer c.notificationMu.Unlock()
	c.notificationHandlers[method] = handler
}

func (c *Client) RegisterServerRequestHandler(method string, handler ServerRequestHandler) {
	c.serverHandlersMu.Lock()
	defer c.serverHandlersMu.Unlock()
	c.serverRequestHandlers[method] = handler
}

func (c *Client) InitializeLSPClient(ctx context.Context, workspaceDir string) (*protocol.InitializeResult, error) {
	initParams := &protocol.InitializeParams{
		WorkspaceFoldersInitializeParams: protocol.WorkspaceFoldersInitializeParams{
			WorkspaceFolders: []protocol.WorkspaceFolder{
				{
					URI:  protocol.URI(protocol.URIFromPath(workspaceDir)),
					Name: workspaceDir,
				},
			},
		},

		XInitializeParams: protocol.XInitializeParams{
			ProcessID: int32(os.Getpid()),
			ClientInfo: &protocol.ClientInfo{
				Name:    "mcp-language-server",
				Version: "0.4.0",
			},
			RootPath: workspaceDir,
			RootURI:  protocol.URIFromPath(workspaceDir),
			Capabilities: protocol.ClientCapabilities{
				Workspace: protocol.WorkspaceClientCapabilities{
					Configuration: true,
					DidChangeConfiguration: protocol.DidChangeConfigurationClientCapabilities{
						DynamicRegistration: true,
					},
					DidChangeWatchedFiles: protocol.DidChangeWatchedFilesClientCapabilities{
						DynamicRegistration:    true,
						RelativePatternSupport: true,
					},
				},
				TextDocument: protocol.TextDocumentClientCapabilities{
					Synchronization: &protocol.TextDocumentSyncClientCapabilities{
						DynamicRegistration: true,
						DidSave:             true,
					},
					Completion: protocol.CompletionClientCapabilities{
						CompletionItem: protocol.ClientCompletionItemOptions{},
					},
					CodeLens: &protocol.CodeLensClientCapabilities{
						DynamicRegistration: true,
					},
					DocumentSymbol: protocol.DocumentSymbolClientCapabilities{},
					CodeAction: protocol.CodeActionClientCapabilities{
						CodeActionLiteralSupport: protocol.ClientCodeActionLiteralOptions{
							CodeActionKind: protocol.ClientCodeActionKindOptions{
								ValueSet: []protocol.CodeActionKind{},
							},
						},
					},
					PublishDiagnostics: protocol.PublishDiagnosticsClientCapabilities{
						VersionSupport: true,
					},
					SemanticTokens: protocol.SemanticTokensClientCapabilities{
						Requests: protocol.ClientSemanticTokensRequestOptions{
							Range: &protocol.Or_ClientSemanticTokensRequestOptions_range{},
							Full:  &protocol.Or_ClientSemanticTokensRequestOptions_full{},
						},
						TokenTypes:     []string{},
						TokenModifiers: []string{},
						Formats:        []protocol.TokenFormat{},
					},
				},
				Window: protocol.WindowClientCapabilities{},
			},
			InitializationOptions: map[string]any{
				"codelenses": map[string]bool{
					"generate":           true,
					"regenerate_cgo":     true,
					"test":               true,
					"tidy":               true,
					"upgrade_dependency": true,
					"vendor":             true,
					"vulncheck":          false,
				},
			},
		},
	}

	var result protocol.InitializeResult
	if err := c.Call(ctx, "initialize", initParams, &result); err != nil {
		return nil, fmt.Errorf("initialize failed: %w", err)
	}

	c.syncKind = extractSyncKind(result.Capabilities.TextDocumentSync)

	if err := c.Notify(ctx, "initialized", struct{}{}); err != nil {
		return nil, fmt.Errorf("initialized notification failed: %w", err)
	}

	// Register handlers
	c.RegisterServerRequestHandler("workspace/applyEdit", HandleApplyEdit)
	c.RegisterServerRequestHandler("workspace/configuration", HandleWorkspaceConfiguration)
	c.RegisterServerRequestHandler("client/registerCapability", HandleRegisterCapability)
	c.RegisterNotificationHandler("window/showMessage", HandleServerMessage)
	c.RegisterNotificationHandler("textDocument/publishDiagnostics",
		func(params json.RawMessage) { HandleDiagnostics(c, params) })

	// Notify the LSP server
	err := c.Initialized(ctx, protocol.InitializedParams{})
	if err != nil {
		return nil, fmt.Errorf("initialization failed: %w", err)
	}

	// LSP sepecific Initialization
	path := strings.ToLower(c.Cmd.Path)
	switch {
	case strings.Contains(path, "typescript-language-server"):
		err := initializeTypescriptLanguageServer(ctx, c, workspaceDir)
		if err != nil {
			return nil, err
		}
	}

	return &result, nil
}

func (c *Client) Close() error {
	// Try to close all open files first
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Attempt to close files but continue shutdown regardless
	c.CloseAllFiles(ctx)

	// Force kill the LSP process if it doesn't exit within timeout
	forcedKill := make(chan struct{})
	go func() {
		select {
		case <-time.After(2 * time.Second):
			lspLogger.Warn("LSP process did not exit within timeout, forcing kill")
			if c.Cmd.Process != nil {
				if err := c.Cmd.Process.Kill(); err != nil {
					lspLogger.Error("Failed to kill process: %v", err)
				} else {
					lspLogger.Info("Process killed successfully")
				}
			}
			close(forcedKill)
		case <-forcedKill:
			// Channel closed from completion path
			return
		}
	}()

	// Close stdin to signal the server
	if err := c.stdin.Close(); err != nil {
		lspLogger.Error("Failed to close stdin: %v", err)
	}

	// Wait for process to exit
	err := c.Cmd.Wait()
	close(forcedKill) // Stop the force kill goroutine

	return err
}

type ServerState int

const (
	StateStarting ServerState = iota
	StateReady
	StateError
)

func (c *Client) WaitForServerReady(ctx context.Context) error {
	for i := 0; i < 30; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		_, err := c.Symbol(ctx, protocol.WorkspaceSymbolParams{Query: ""})
		if err == nil {
			lspLogger.Info("LSP server ready (workspace/symbol responded)")
			return nil
		}

		lspLogger.Debug("LSP server not ready yet, retrying in 500ms... (%d/30)", i+1)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return fmt.Errorf("LSP server not ready after 15s")
}

type OpenFileInfo struct {
	Version int32
	URI     protocol.DocumentUri
	// Content is the document text last synced to the server, used to
	// compute incremental changes.
	Content []byte
}

func (c *Client) OpenFile(ctx context.Context, filepath string) error {
	uri := protocol.URIFromPath(filepath)

	c.openFilesMu.Lock()
	if _, exists := c.openFiles[string(uri)]; exists {
		c.openFilesMu.Unlock()
		return nil // Already open
	}
	c.openFilesMu.Unlock()

	// Skip files that do not exist or cannot be read
	content, err := os.ReadFile(filepath)
	if err != nil {
		return fmt.Errorf("error reading file: %w", err)
	}

	params := protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{
			URI:        protocol.DocumentUri(uri),
			LanguageID: DetectLanguageID(string(uri)),
			Version:    1,
			Text:       string(content),
		},
	}

	if err := c.Notify(ctx, "textDocument/didOpen", params); err != nil {
		return err
	}

	c.openFilesMu.Lock()
	c.openFiles[string(uri)] = &OpenFileInfo{
		Version: 1,
		URI:     protocol.DocumentUri(uri),
		Content: content,
	}
	c.openFilesMu.Unlock()

	lspLogger.Debug("Opened file: %s", filepath)

	return nil
}

func (c *Client) NotifyChange(ctx context.Context, filepath string) error {
	uri := protocol.URIFromPath(filepath)

	content, err := os.ReadFile(filepath)
	if err != nil {
		return fmt.Errorf("error reading file: %w", err)
	}

	c.openFilesMu.Lock()
	fileInfo, isOpen := c.openFiles[string(uri)]
	if !isOpen {
		c.openFilesMu.Unlock()
		return fmt.Errorf("cannot notify change for unopened file: %s", filepath)
	}

	fileInfo.Version++
	version := fileInfo.Version
	oldContent := fileInfo.Content
	c.openFilesMu.Unlock()

	var changes []protocol.TextDocumentContentChangeEvent

	if c.syncKind == protocol.Incremental && oldContent != nil {
		changes = computeIncrementalChanges(oldContent, content)
		if len(changes) == 0 {
			// Content identical to the synced snapshot; nothing to send.
			return nil
		}
	} else {
		changes = []protocol.TextDocumentContentChangeEvent{
			{
				Value: protocol.TextDocumentContentChangeWholeDocument{
					Text: string(content),
				},
			},
		}
	}

	params := protocol.DidChangeTextDocumentParams{
		TextDocument: protocol.VersionedTextDocumentIdentifier{
			TextDocumentIdentifier: protocol.TextDocumentIdentifier{
				URI: protocol.DocumentUri(uri),
			},
			Version: version,
		},
		ContentChanges: changes,
	}

	if err := c.Notify(ctx, "textDocument/didChange", params); err != nil {
		return err
	}

	c.openFilesMu.Lock()
	fileInfo.Content = content
	c.openFilesMu.Unlock()
	return nil
}

// computeIncrementalChanges diffs old and new document contents and returns
// a single ranged change covering the differing region (common prefix/suffix
// trimmed). Region boundaries are backed off to UTF-8 rune starts so neither
// the range nor the replacement text splits a rune. Positions use UTF-16
// code units, the LSP default position encoding (this client does not
// negotiate general.positionEncodings).
func computeIncrementalChanges(oldContent, newContent []byte) []protocol.TextDocumentContentChangeEvent {
	if bytes.Equal(oldContent, newContent) {
		return nil
	}

	minLen := len(oldContent)
	if len(newContent) < minLen {
		minLen = len(newContent)
	}

	start := 0
	for start < minLen && oldContent[start] == newContent[start] {
		start++
	}
	end := 0
	for end < minLen-start && oldContent[len(oldContent)-1-end] == newContent[len(newContent)-1-end] {
		end++
	}

	// Back the boundaries off to rune starts. The shared prefix (old==new up
	// to start) and shared suffix make a boundary valid for both contents.
	for start > 0 && start < len(oldContent) && !utf8.RuneStart(oldContent[start]) {
		start--
	}
	for end > 0 && !utf8.RuneStart(oldContent[len(oldContent)-end]) {
		end--
	}

	if start == 0 && end == 0 {
		// Nothing in common: a whole-document change is cheaper to encode.
		return []protocol.TextDocumentContentChangeEvent{
			{
				Value: protocol.TextDocumentContentChangeWholeDocument{
					Text: string(newContent),
				},
			},
		}
	}

	oldEnd := len(oldContent) - end
	newEnd := len(newContent) - end
	return []protocol.TextDocumentContentChangeEvent{
		{
			Value: protocol.TextDocumentContentChangePartial{
				Range: &protocol.Range{
					Start: positionAt(oldContent, start),
					End:   positionAt(oldContent, oldEnd),
				},
				Text: string(newContent[start:newEnd]),
			},
		},
	}
}

// positionAt converts a byte offset into an LSP position: line is 0-based,
// character counts UTF-16 code units since the last newline.
func positionAt(content []byte, offset int) protocol.Position {
	var line, char uint32
	for i := 0; i < offset && i < len(content); {
		r, size := utf8.DecodeRune(content[i:])
		if r == '\n' {
			line++
			char = 0
		} else if r > 0xFFFF {
			char += 2 // surrogate pair in UTF-16
		} else {
			char++
		}
		i += size
	}
	return protocol.Position{Line: line, Character: char}
}

func (c *Client) CloseFile(ctx context.Context, filepath string) error {
	uri := protocol.URIFromPath(filepath)

	c.openFilesMu.Lock()
	if _, exists := c.openFiles[string(uri)]; !exists {
		c.openFilesMu.Unlock()
		return nil // Already closed
	}
	c.openFilesMu.Unlock()

	params := protocol.DidCloseTextDocumentParams{
		TextDocument: protocol.TextDocumentIdentifier{
			URI: protocol.DocumentUri(uri),
		},
	}
	lspLogger.Debug("Closing file: %s", params.TextDocument.URI.Dir())
	if err := c.Notify(ctx, "textDocument/didClose", params); err != nil {
		return err
	}

	c.openFilesMu.Lock()
	delete(c.openFiles, string(uri))
	c.openFilesMu.Unlock()

	return nil
}

func (c *Client) IsFileOpen(filepath string) bool {
	uri := protocol.URIFromPath(filepath)
	c.openFilesMu.RLock()
	defer c.openFilesMu.RUnlock()
	_, exists := c.openFiles[string(uri)]
	return exists
}

// CloseAllFiles closes all currently open files
func (c *Client) CloseAllFiles(ctx context.Context) {
	c.openFilesMu.Lock()
	filesToClose := make([]string, 0, len(c.openFiles))

	// First collect all URIs that need to be closed
	for uri := range c.openFiles {
		documentURI, err := protocol.ParseDocumentUri(uri)
		if err != nil {
			lspLogger.Error("Error parsing open file URI %s: %v", uri, err)
			continue
		}
		filesToClose = append(filesToClose, documentURI.Path())
	}
	c.openFilesMu.Unlock()

	// Then close them all
	for _, filePath := range filesToClose {
		err := c.CloseFile(ctx, filePath)
		if err != nil {
			lspLogger.Error("Error closing file %s: %v", filePath, err)
		}
	}

	lspLogger.Debug("Closed %d files", len(filesToClose))
}

func (c *Client) GetFileDiagnostics(uri protocol.DocumentUri) []protocol.Diagnostic {
	c.diagnosticsMu.RLock()
	defer c.diagnosticsMu.RUnlock()

	return c.diagnostics[uri]
}

// WaitForDiagnostics blocks until publishDiagnostics arrives for the given URI
// or the timeout expires. Returns true if diagnostics were received before timeout.
func (c *Client) WaitForDiagnostics(uri protocol.DocumentUri, timeout time.Duration) bool {
	ch := make(chan struct{}, 1)

	c.diagWaitersMu.Lock()
	c.diagWaiters[uri] = append(c.diagWaiters[uri], ch)
	c.diagWaitersMu.Unlock()

	select {
	case <-ch:
		return true
	case <-time.After(timeout):
		c.diagWaitersMu.Lock()
		waiters := c.diagWaiters[uri]
		for i, w := range waiters {
			if w == ch {
				c.diagWaiters[uri] = append(waiters[:i], waiters[i+1:]...)
				break
			}
		}
		c.diagWaitersMu.Unlock()
		return false
	}
}

// notifyDiagnosticsArrived wakes all goroutines waiting for diagnostics for the given URI.
func (c *Client) notifyDiagnosticsArrived(uri protocol.DocumentUri) {
	c.diagWaitersMu.Lock()
	waiters := c.diagWaiters[uri]
	delete(c.diagWaiters, uri)
	c.diagWaitersMu.Unlock()

	for _, ch := range waiters {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func extractSyncKind(sync interface{}) protocol.TextDocumentSyncKind {
	if sync == nil {
		return protocol.Full
	}
	switch v := sync.(type) {
	case float64:
		return protocol.TextDocumentSyncKind(uint32(v))
	case protocol.TextDocumentSyncKind:
		return v
	case map[string]interface{}:
		if change, ok := v["change"]; ok {
			if n, ok := change.(float64); ok {
				return protocol.TextDocumentSyncKind(uint32(n))
			}
		}
	}
	return protocol.Full
}
