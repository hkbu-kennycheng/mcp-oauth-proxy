package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/obot-platform/mcp-oauth-proxy/pkg/types"
	"github.com/stretchr/testify/assert"
	"golang.org/x/oauth2"
)

func TestGenericProvider_RefreshTokenParams(t *testing.T) {
	t.Run("TestGoogleRefreshTokenParams", func(t *testing.T) {
		provider := &GenericProvider{
			authorizeURL: "https://accounts.google.com/o/oauth2/v2/auth",
			httpClient: &http.Client{
				Timeout: 30 * time.Second,
			},
		}

		authURL := provider.GetAuthorizationURL("test_client", "https://test.example.com/callback", "openid profile email", "test_state")

		assert.Contains(t, authURL, "access_type=offline")
		assert.Contains(t, authURL, "prompt=consent")
		assert.Contains(t, authURL, "client_id=test_client")
		assert.Contains(t, authURL, "scope=openid+profile+email")
		assert.Contains(t, authURL, "state=test_state")
	})

	t.Run("TestGenericProviderNoSpecialParams", func(t *testing.T) {
		provider := &GenericProvider{
			authorizeURL: "https://github.com/login/oauth/authorize",
			httpClient: &http.Client{
				Timeout: 30 * time.Second,
			},
		}

		authURL := provider.GetAuthorizationURL("test_client", "https://test.example.com/callback", "read:user user:email", "test_state")

		// Should not contain Google or Microsoft specific parameters
		assert.NotContains(t, authURL, "response_mode=query")
		assert.NotContains(t, authURL, "offline_access")

		// Should contain standard OAuth parameters
		assert.Contains(t, authURL, "client_id=test_client")
		assert.Contains(t, authURL, "scope=read%3Auser+user%3Aemail")
		assert.Contains(t, authURL, "state=test_state")
	})
}

func TestGenericProvider_GitHubEndpointsFallback(t *testing.T) {
	provider := NewGenericProvider("https://github.com/login/oauth/authorize")
	err := provider.discoverEndpoints()
	assert.NoError(t, err)
	assert.NotNil(t, provider.metadata)
	assert.Equal(t, "https://github.com/login/oauth/access_token", provider.metadata.TokenEndpoint)
	assert.Equal(t, "https://api.github.com/user", provider.metadata.UserinfoEndpoint)
}

func TestGenericProvider_UserAgentAndAcceptHeaders(t *testing.T) {
	var capturedUA string
	var capturedAccept string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedUA = r.Header.Get("User-Agent")
		capturedAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"mock_access_token","token_type":"bearer"}`))
	}))
	defer server.Close()

	provider := NewGenericProvider(server.URL + "/authorize")

	provider.metadata = &types.OAuthMetadata{
		AuthorizationEndpoint: server.URL + "/authorize",
		TokenEndpoint:         server.URL + "/token",
	}

	// Test ExchangeCodeForToken passes custom client in context
	tok, err := provider.ExchangeCodeForToken(context.Background(), "mock_code", "client_id", "client_secret", "http://redirect")
	assert.NoError(t, err)
	assert.NotNil(t, tok)
	assert.Equal(t, defaultUserAgent, capturedUA)
	assert.Equal(t, "application/json", capturedAccept)
}

func TestGenericProvider_GitHubAuthStyleInParams(t *testing.T) {
	provider := NewGenericProvider("https://github.com/login/oauth/authorize")
	_ = provider.discoverEndpoints()

	cfg := provider.buildOAuth2Config(provider.metadata.AuthorizationEndpoint, "id", "secret", "http://redirect", "read:user")
	assert.Equal(t, oauth2.AuthStyleInParams, cfg.Endpoint.AuthStyle)
}
