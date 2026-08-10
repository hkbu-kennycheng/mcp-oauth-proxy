package transport

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obot-platform/mcp-oauth-proxy/pkg/tokens"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// simpleMCPServer is a minimal Go program that simulates an MCP server over stdio.
// It reads JSON-RPC messages from stdin and writes responses to stdout.
const simpleMCPServer = `package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

func main() {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var msg map[string]interface{}
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			fmt.Fprintf(os.Stderr, "invalid json: %v\n", err)
			continue
		}

		method, _ := msg["method"].(string)
		id, _ := msg["id"]

		var resp map[string]interface{}
		switch method {
		case "initialize":
			resp = map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      id,
				"result": map[string]interface{}{
					"protocolVersion": "2024-11-05",
					"capabilities": map[string]interface{}{
						"prompts":   map[string]interface{}{},
						"resources": map[string]interface{}{},
						"tools":     map[string]interface{}{},
					},
					"serverInfo": map[string]interface{}{
						"name":    "test-mcp-server",
						"version": "1.0.0",
					},
				},
			}
		default:
			resp = map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      id,
				"result":  map[string]interface{}{"status": "ok"},
			}
		}

		out, _ := json.Marshal(resp)
		fmt.Println(string(out))
	}
}
`

func buildMockMCPServer(t *testing.T) string {
	t.Helper()

	// Create a temporary directory for the mock server
	tmpDir := t.TempDir()
	serverPath := filepath.Join(tmpDir, "mock_mcp_server.go")
	binaryPath := filepath.Join(tmpDir, "mock_mcp_server")

	// Write the mock server Go file
	err := os.WriteFile(serverPath, []byte(simpleMCPServer), 0644)
	require.NoError(t, err)

	// Build the mock server binary
	cmd := exec.Command("go", "build", "-o", binaryPath, serverPath)
	cmd.Dir = tmpDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Skipf("Skipping test: failed to build mock MCP server: %v - %s", err, string(output))
		return ""
	}

	return binaryPath
}

func TestStdioTransport_Integration(t *testing.T) {
	binaryPath := buildMockMCPServer(t)
	if binaryPath == "" {
		return
	}

	// Create the stdio transport
	stdioTransport, err := NewStdioTransport(binaryPath, nil, "")
	require.NoError(t, err)
	require.NotNil(t, stdioTransport)
	defer stdioTransport.Close()

	t.Run("InitializeRequest", func(t *testing.T) {
		initializeRequest := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{"prompts":{}},"clientInfo":{"name":"test","version":"1.0"}}}`

		req := httptest.NewRequest("POST", "/mcp", strings.NewReader(initializeRequest))
		recorder := httptest.NewRecorder()

		tokenInfo := &tokens.TokenInfo{
			UserID: "user123",
			Props: map[string]any{
				"user_id":      "user123",
				"email":        "user@example.com",
				"name":         "John Doe",
				"access_token": "token123",
			},
		}

		stdioTransport.ServeHTTP(recorder, req, tokenInfo)

		resp := recorder.Result()
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		var result map[string]interface{}
		err = json.Unmarshal(body, &result)
		require.NoError(t, err)

		assert.Equal(t, "2.0", result["jsonrpc"])
		assert.Equal(t, float64(1), result["id"])

		resultMap, ok := result["result"].(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, "test-mcp-server", resultMap["serverInfo"].(map[string]interface{})["name"])
	})

	t.Run("GenericRequestAfterInitialize", func(t *testing.T) {
		// This tests that the MCP server stays alive after initialize
		// and can handle subsequent requests
		simpleRequest := `{"jsonrpc":"2.0","id":2,"method":"ping"}`

		req := httptest.NewRequest("POST", "/mcp", strings.NewReader(simpleRequest))
		recorder := httptest.NewRecorder()

		stdioTransport.ServeHTTP(recorder, req, nil)

		resp := recorder.Result()
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		var result map[string]interface{}
		err = json.Unmarshal(body, &result)
		require.NoError(t, err)

		assert.Equal(t, "2.0", result["jsonrpc"])

		resultMap, ok := result["result"].(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, "ok", resultMap["status"])
	})

	t.Run("InvalidCommandReturnsError", func(t *testing.T) {
		// Create transport with invalid command
		invalidTransport, err := NewStdioTransport("nonexistent-command-xyz", nil, "")
		require.NoError(t, err)
		defer invalidTransport.Close()

		req := httptest.NewRequest("POST", "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
		recorder := httptest.NewRecorder()

		invalidTransport.ServeHTTP(recorder, req, nil)

		resp := recorder.Result()
		assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	})
}
