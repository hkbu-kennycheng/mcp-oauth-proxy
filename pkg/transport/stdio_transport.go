package transport

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/obot-platform/mcp-oauth-proxy/pkg/tokens"
)

// StdioTransport implements Transport for stdio-based upstream MCP servers
type StdioTransport struct {
	commands []string       // Command and arguments to execute
	env      []string       // Additional environment variables
	cwd      string         // Working directory (optional)
}

// NewStdioTransport creates a new stdio transport
func NewStdioTransport(command string, env map[string]string, cwd string) (*StdioTransport, error) {
	if command == "" {
		return nil, errors.New("mcp server command is required for stdio transport")
	}

	// Parse the command - split on spaces but preserve quoted strings
	commands, err := parseCommand(command)
	if err != nil {
		return nil, fmt.Errorf("invalid mcp server command: %w", err)
	}

	if len(commands) == 0 {
		return nil, errors.New("mcp server command is empty")
	}

	// Convert env map to []string
	var envList []string
	if env != nil {
		for k, v := range env {
			envList = append(envList, k+"="+v)
		}
	}

	return &StdioTransport{
		commands: commands,
		env:      envList,
		cwd:      cwd,
	}, nil
}

// parseCommand splits a command string while respecting quotes
func parseCommand(command string) ([]string, error) {
	// Use shell-like parsing
	parts := strings.Fields(command)
	if len(parts) == 0 {
		return nil, errors.New("empty command")
	}
	return parts, nil
}

// ServeHTTP handles an HTTP request by forwarding it to the stdio MCP server
func (t *StdioTransport) ServeHTTP(w http.ResponseWriter, r *http.Request, tokenInfo *tokens.TokenInfo) {
	ctx := r.Context()

	// Spawn a new MCP server subprocess for this request
	session, err := t.spawnProcess(ctx)
	if err != nil {
		log.Printf("Failed to spawn MCP server: %v", err)
		http.Error(w, fmt.Sprintf("Failed to start MCP server: %v", err), http.StatusInternalServerError)
		return
	}
	defer session.Terminate()

	// Read the request body
	body, err := readRequestBody(r)
	if err != nil {
		log.Printf("Failed to read request body: %v", err)
		http.Error(w, fmt.Sprintf("Failed to read request: %v", err), http.StatusBadRequest)
		return
	}

	// Check if this is an initialize request and inject user context
	if isInitializeRequest(body) {
		body = injectUserContext(body, tokenInfo)
		log.Printf("Injected user context into initialize request")
	}

	log.Printf("Sending %d bytes to MCP server", len(body))

	// Send the message to the MCP server
	response, err := t.sendMessage(ctx, session, body)
	if err != nil {
		log.Printf("Failed to get response from MCP server: %v", err)
		http.Error(w, fmt.Sprintf("MCP server error: %v", err), http.StatusBadGateway)
		return
	}

	log.Printf("Received %d bytes from MCP server", len(response))

	// Set content type and write response
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(response)
}

// StdioSession manages a single subprocess session
type StdioSession struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	ctx    context.Context
	cancel context.CancelFunc

	readMu  sync.Mutex
	writeMu sync.Mutex
}

// spawnProcess starts a new MCP server subprocess
func (t *StdioTransport) spawnProcess(ctx context.Context) (*StdioSession, error) {
	// Create a cancellable context for this subprocess
	sessionCtx, cancel := context.WithCancel(ctx)

	// Set up the command
	cmd := exec.CommandContext(sessionCtx, t.commands[0], t.commands[1:]...)

	// Set environment variables
	if t.env != nil {
		if cmd.Env == nil {
			cmd.Env = []string{}
		}
		cmd.Env = append(cmd.Env, t.env...)
	}

	// Set working directory if specified
	if t.cwd != "" {
		cmd.Dir = t.cwd
	}

	// Create pipes for stdin and stdout
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create stdin pipe: %w", err)
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	// Capture stderr for logging (discard for now, could be logged)
	cmd.Stderr = nil

	// Start the subprocess
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("failed to start MCP server: %w", err)
	}

	session := &StdioSession{
		cmd:    cmd,
		stdin:  stdinPipe,
		stdout: stdoutPipe,
		ctx:    sessionCtx,
		cancel: cancel,
	}

	// Start a goroutine to wait for process exit
	go func() {
		<-sessionCtx.Done()
		// Close stdin to signal the process to exit naturally
		if session.stdin != nil {
			session.stdin.Close()
		}
		// Wait briefly for the process to exit on its own
		done := make(chan struct{})
		go func() {
			session.cmd.Wait()
			close(done)
		}()
		select {
		case <-done:
			// Process exited naturally
		case <-time.After(3 * time.Second):
			// Force kill if it doesn't exit within 3 seconds
			if session.cmd.Process != nil {
				session.cmd.Process.Kill()
			}
			session.cmd.Wait()
		}
	}()

	return session, nil
}

// sendMessage sends a message to the MCP server's stdin and reads the response from stdout
func (t *StdioTransport) sendMessage(ctx context.Context, session *StdioSession, message []byte) ([]byte, error) {
	// Write the message to stdin followed by newline
	session.writeMu.Lock()
	_, err := session.stdin.Write(append(message, '\n'))
	if err != nil {
		session.writeMu.Unlock()
		return nil, fmt.Errorf("failed to write to MCP server: %w", err)
	}

	// Flush the stdin pipe to ensure the message is sent immediately
	if flusher, ok := session.stdin.(interface{ Flush() error }); ok {
		flushErr := flusher.Flush()
		if flushErr != nil {
			session.writeMu.Unlock()
			return nil, fmt.Errorf("failed to flush MCP server input: %w", flushErr)
		}
	}
	session.writeMu.Unlock()

	// Set up a reader for stdout
	reader := bufio.NewReader(session.stdout)

	// Read response from stdout - wait for a single line (JSON message)
	session.readMu.Lock()
	defer session.readMu.Unlock()

	// Read one line (one JSON message)
	response, err := reader.ReadBytes('\n')
	if err != nil {
		if err.Error() == "EOF" {
			return nil, fmt.Errorf("MCP server closed connection unexpectedly")
		}
		return nil, fmt.Errorf("failed to read from MCP server: %w", err)
	}

	// Trim the trailing newline/carriage return
	response = bytes.TrimRight(response, "\n\r")

	if len(response) == 0 {
		return nil, fmt.Errorf("empty response from MCP server")
	}

	// Validate that response looks like JSON
	if len(response) > 0 && response[0] != '{' && response[0] != '[' {
		return nil, fmt.Errorf("MCP server response is not valid JSON: %s", string(response))
	}

	return response, nil
}

// Terminate closes the stdin pipe to signal end of input to the MCP server.
// The subprocess will naturally exit after finishing its response.
func (s *StdioSession) Terminate() {
	// Close stdin to signal we're done sending input
	// This allows the MCP server to see EOF and finish its response naturally
	if s.stdin != nil {
		s.stdin.Close()
	}
}

// readRequestBody reads the HTTP request body
func readRequestBody(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return []byte("{}"), nil
	}
	defer r.Body.Close()

	buf, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	if len(buf) == 0 {
		return []byte("{}"), nil
	}
	return buf, nil
}

// isInitializeRequest checks if the request body is an MCP initialize request
func isInitializeRequest(body []byte) bool {
	var msg struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal(body, &msg); err != nil {
		return false
	}
	return msg.Method == "initialize"
}

// injectUserContext injects user identity into the initialize request
func injectUserContext(body []byte, tokenInfo *tokens.TokenInfo) []byte {
	if tokenInfo == nil || tokenInfo.Props == nil {
		return body
	}

	var msg map[string]interface{}
	if err := json.Unmarshal(body, &msg); err != nil {
		return body
	}

	// Get params, or create them
	params, ok := msg["params"].(map[string]interface{})
	if !ok {
		params = make(map[string]interface{})
	}

	// Inject user context
	userContext := make(map[string]interface{})
	for _, key := range []string{"user_id", "email", "name", "access_token"} {
		if val, ok := tokenInfo.Props[key].(string); ok {
			userContext[key] = val
		}
	}

	if len(userContext) > 0 {
		params["userContext"] = userContext
	}

	msg["params"] = params

	modifiedBody, err := json.Marshal(msg)
	if err != nil {
		return body
	}
	return modifiedBody
}

// Close is a no-op for stdio transport (sessions are managed per-request)
func (t *StdioTransport) Close() error {
	return nil
}
