package proxy

import (
	"testing"

	"github.com/obot-platform/mcp-oauth-proxy/pkg/types"
	"github.com/stretchr/testify/assert"
)

func TestNewOAuthProxy_StdioMode(t *testing.T) {
	t.Run("StdioModeCreatesTransport", func(t *testing.T) {
		config := &types.Config{
			Mode:              ModeProxy,
			MCPServerCommand:  "echo test",
			OAuthClientID:     "test_client_id",
			OAuthClientSecret: "test_client_secret",
			OAuthAuthorizeURL: "https://accounts.google.com",
			ScopesSupported:   "openid,profile,email",
		}
		p, err := NewOAuthProxy(config)
		// This will succeed if transport creation succeeds
		if err != nil {
			// It might fail if "echo" isn't available, but let's just check
			// that it doesn't fail for configuration reasons
			assert.NotContains(t, err.Error(), "failed to create stdio transport")
			return
		}
		defer p.Close()

		// Verify transport was created
		assert.NotNil(t, p.transport)
	})

	t.Run("StdioModeWithInvalidCommand", func(t *testing.T) {
		config := &types.Config{
			Mode:              ModeProxy,
			MCPServerCommand:  "",
			OAuthClientID:     "test_client_id",
			OAuthClientSecret: "test_client_secret",
			OAuthAuthorizeURL: "https://accounts.google.com",
			ScopesSupported:   "openid,profile,email",
		}
		_, err := NewOAuthProxy(config)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "either mcp-server-url or mcp-server-command is required")
	})
}
