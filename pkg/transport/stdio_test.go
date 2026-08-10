package transport

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/obot-platform/mcp-oauth-proxy/pkg/tokens"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockMCPServerScript creates a simple MCP server script that echoes JSON-RPC messages
const mockMCPServerScript = `#!/bin/sh
# Simple MCP server that responds to JSON-RPC messages
while IFS= read -r line; do
	if [ -z "$line" ]; then
		continue
	fi
	# Parse the method from the JSON message
	method=$(echo "$line" | python3 -c "import sys,json; print(json.load(sys.stdin).get('method',''))" 2>/dev/null || echo "")
	if [ "$method" = "initialize" ]; then
		# Return initialize response
		echo '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05","capabilities":{"prompts":{},"resources":{}},"serverInfo":{"name":"mock-mcp-server","version":"1.0"}}}'
	elif [ "$method" = "ping" ]; then
		echo '{"jsonrpc":"2.0","id":2,"result":{"status":"ok"}}'
	else
		# Echo back with success
		echo '{"jsonrpc":"2.0","id":1,"result":{"content":"ok"}}'
	fi
done
`

func TestNewStdioTransport_EmptyCommand(t *testing.T) {
	_, err := NewStdioTransport("", nil, "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "command is required")
}

func TestNewStdioTransport_InvalidCommand(t *testing.T) {
	// This test verifies command parsing - empty command string
	_, err := NewStdioTransport("", nil, "")
	assert.Error(t, err)
}

func TestNewStdioTransport_WithEnv(t *testing.T) {
	env := map[string]string{
		"API_KEY": "key123",
		"DEBUG":   "true",
	}

	transport, err := NewStdioTransport("mock-server", env, "/tmp")
	require.NoError(t, err)
	assert.Equal(t, []string{"API_KEY=key123", "DEBUG=true"}, transport.env)
	assert.Equal(t, "/tmp", transport.cwd)
}

func TestNewStdioTransport_ComplexCommand(t *testing.T) {
	tests := []struct {
		name    string
		cmd     string
		want    []string
		wantErr bool
	}{
		{
			name: "PythonWithArgs",
			cmd:  "python3 /path/to/server.py --port 8080",
			want: []string{"python3", "/path/to/server.py", "--port", "8080"},
		},
		{
			name: "NodeWithArgs",
			cmd:  "node /path/to/server.js --debug",
			want: []string{"node", "/path/to/server.js", "--debug"},
		},
		{
			name: "SimpleCommand",
			cmd:  "mcp-server",
			want: []string{"mcp-server"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseCommand(tt.cmd)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.want, result)
			}
		})
	}
}

func TestParseCommand(t *testing.T) {
	tests := []struct {
		name    string
		cmd     string
		want    []string
		wantErr bool
	}{
		{
			name: "SimpleCommand",
			cmd:  "echo hello",
			want: []string{"echo", "hello"},
		},
		{
			name: "MultipleArgs",
			cmd:  "python3 /path/to/server.py --port 8080",
			want: []string{"python3", "/path/to/server.py", "--port", "8080"},
		},
		{
			name:    "EmptyCommand",
			cmd:     "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseCommand(tt.cmd)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

func TestIsInitializeRequest(t *testing.T) {
	tests := []struct {
		name     string
		body     []byte
		expected bool
	}{
		{
			name:     "InitializeRequest",
			body:     []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`),
			expected: true,
		},
		{
			name:     "NotInitializeRequest",
			body:     []byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}`),
			expected: false,
		},
		{
			name:     "InvalidJSON",
			body:     []byte(`invalid json`),
			expected: false,
		},
		{
			name:     "EmptyBody",
			body:     []byte(`{}`),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isInitializeRequest(tt.body)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestInjectUserContext(t *testing.T) {
	tests := []struct {
		name       string
		body       []byte
		tokenInfo  *tokens.TokenInfo
		validateFn func(t *testing.T, body []byte)
	}{
		{
			name: "InjectUserContext",
			body: []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`),
			tokenInfo: &tokens.TokenInfo{
				UserID: "user123",
				Props: map[string]any{
					"user_id":      "user123",
					"email":        "user@example.com",
					"name":         "John Doe",
					"access_token": "token123",
				},
			},
			validateFn: func(t *testing.T, body []byte) {
				var msg map[string]interface{}
				err := json.Unmarshal(body, &msg)
				require.NoError(t, err)

				params, ok := msg["params"].(map[string]interface{})
				require.True(t, ok)

				userContext, ok := params["userContext"].(map[string]interface{})
				require.True(t, ok)

				assert.Equal(t, "user123", userContext["user_id"])
				assert.Equal(t, "user@example.com", userContext["email"])
				assert.Equal(t, "John Doe", userContext["name"])
				assert.Equal(t, "token123", userContext["access_token"])
			},
		},
		{
			name: "NoTokenInfo",
			body: []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`),
			tokenInfo: nil,
			validateFn: func(t *testing.T, body []byte) {
				// Should return body unchanged
				var msg map[string]interface{}
				err := json.Unmarshal(body, &msg)
				require.NoError(t, err)

				params, ok := msg["params"].(map[string]interface{})
				require.True(t, ok)

				// userContext should not be present
				_, hasContext := params["userContext"]
				assert.False(t, hasContext)
			},
		},
		{
			name: "PartialProps",
			body: []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`),
			tokenInfo: &tokens.TokenInfo{
				UserID: "user123",
				Props: map[string]any{
					"user_id": "user123",
					// Missing other fields
				},
			},
			validateFn: func(t *testing.T, body []byte) {
				var msg map[string]interface{}
				err := json.Unmarshal(body, &msg)
				require.NoError(t, err)

				params := msg["params"].(map[string]interface{})
				userContext := params["userContext"].(map[string]interface{})

				assert.Equal(t, "user123", userContext["user_id"])
				// Other fields should not be present
				_, hasEmail := userContext["email"]
				assert.False(t, hasEmail)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := injectUserContext(tt.body, tt.tokenInfo)
			tt.validateFn(t, result)
		})
	}
}

func TestReadRequestBody(t *testing.T) {
	tests := []struct {
		name     string
		body     func() io.ReadCloser
		expected string
	}{
		{
			name:     "ValidBody",
			body:     func() io.ReadCloser { return io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`)) },
			expected: `{"jsonrpc":"2.0","id":1,"method":"initialize"}`,
		},
		{
			name:     "EmptyBody",
			body:     func() io.ReadCloser { return io.NopCloser(strings.NewReader("")) },
			expected: `{}`,
		},
		{
			name:     "NilBody",
			body:     func() io.ReadCloser { return nil },
			expected: `{}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.name == "NilBody" {
				req := httptest.NewRequest("POST", "/test", nil)
				result, err := readRequestBody(req)
				require.NoError(t, err)
				assert.Equal(t, tt.expected, string(result))
				return
			}
			req := httptest.NewRequest("POST", "/test", tt.body())
			result, err := readRequestBody(req)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, string(result))
		})
	}
}

