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

	"github.com/obot-platform/mcp-oauth-proxy/pkg/tokens"
)

// StdioTransport implements Transport for stdio-based upstream MCP servers
type StdioTransport struct {
	commands []string
	env      []string
	cwd      string

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser

	mu         sync.Mutex
	started    bool
	startError error
}

// NewStdioTransport creates a new stdio transport
func NewStdioTransport(command string, env map[string]string, cwd string) (*StdioTransport, error) {
	if command == "" {
		return nil, errors.New("mcp server command is required for stdio transport")
	}

	commands, err := parseCommand(command)
	if err != nil {
		return nil, fmt.Errorf("invalid mcp server command: %w", err)
	}

	if len(commands) == 0 {
		return nil, errors.New("mcp server command is empty")
	}

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

// Start launches the MCP server subprocess
func (t *StdioTransport) Start() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.started {
		return t.startError
	}

	cmd := exec.Command(t.commands[0], t.commands[1:]...)

	if t.env != nil {
		cmd.Env = append(cmd.Env, t.env...)
	}

	if t.cwd != "" {
		cmd.Dir = t.cwd
	}

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		t.started = true
		t.startError = fmt.Errorf("failed to create stdin pipe: %w", err)
		return t.startError
	}
	t.stdin = stdinPipe

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		t.started = true
		t.startError = fmt.Errorf("failed to create stdout pipe: %w", err)
		return t.startError
	}
	t.stdout = stdoutPipe

	if err := cmd.Start(); err != nil {
		t.started = true
		t.startError = fmt.Errorf("failed to start MCP server: %w", err)
		return t.startError
	}

	t.cmd = cmd
	t.started = true
	t.startError = nil

	// Wait for process to exit in a goroutine
	go func() {
		cmd.Wait()
	}()

	return nil
}

// ServeHTTP handles an HTTP request by forwarding it to the stdio MCP server
func (t *StdioTransport) ServeHTTP(w http.ResponseWriter, r *http.Request, tokenInfo *tokens.TokenInfo) {
	// Ensure the MCP server is started
	if err := t.Start(); err != nil {
		log.Printf("Failed to start MCP server: %v", err)
		http.Error(w, fmt.Sprintf("Failed to start MCP server: %v", err), http.StatusInternalServerError)
		return
	}

	ctx := r.Context()

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
	response, err := t.sendMessage(ctx, body)
	if err != nil {
		log.Printf("Failed to get response from MCP server: %v", err)
		http.Error(w, fmt.Sprintf("MCP server error: %v", err), http.StatusBadGateway)
		return
	}

	log.Printf("Received %d bytes from MCP server", len(response))

	// If this was an initialize request, also send the initialized notification
	// This is required by the MCP protocol to complete the handshake
	if isInitializeRequest(body) {
		initializedMsg := []byte(`{"jsonrpc":"2.0","id":0,"method":"initialized","params":{}}`)

		_, initErr := t.sendMessage(ctx, initializedMsg)
		if initErr != nil {
			log.Printf("Warning: failed to send initialized notification: %v", initErr)
		}
	}

	// Set content type and write response
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(response)
}

// sendMessage sends a message to the MCP server and returns the response
func (t *StdioTransport) sendMessage(ctx context.Context, message []byte) ([]byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.stdin == nil || t.stdout == nil {
		return nil, errors.New("MCP server not started")
	}

	// Write the message to stdin followed by newline
	_, err := t.stdin.Write(append(message, '\n'))
	if err != nil {
		return nil, fmt.Errorf("failed to write to MCP server: %w", err)
	}

	// Set up a reader for stdout
	reader := bufio.NewReader(t.stdout)

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

// Close shuts down the MCP server subprocess
func (t *StdioTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.stdin != nil {
		t.stdin.Close()
	}

	if t.stdout != nil {
		t.stdout.Close()
	}

	if t.cmd != nil && t.cmd.Process != nil {
		t.cmd.Process.Kill()
	}

	return nil
}

// parseCommand splits a command string into parts
func parseCommand(command string) ([]string, error) {
	parts := strings.Fields(command)
	if len(parts) == 0 {
		return nil, errors.New("empty command")
	}
	return parts, nil
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