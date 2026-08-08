package callback

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/obot-platform/mcp-oauth-proxy/pkg/providers"
	"github.com/obot-platform/mcp-oauth-proxy/pkg/types"
	"github.com/stretchr/testify/assert"
	"golang.org/x/oauth2"
)

type mockStore struct {
	authRequest map[string]any
}

func (m *mockStore) StoreGrant(grant *types.Grant) error                             { return nil }
func (m *mockStore) StoreAuthCode(code, grantID, userID string) error               { return nil }
func (m *mockStore) GetAuthRequest(key string) (map[string]any, error)               { return m.authRequest, nil }
func (m *mockStore) DeleteAuthRequest(key string) error                              { return nil }
func (m *mockStore) StoreToken(token *types.TokenData) error                         { return nil }

type mockProvider struct {
	email string
}

func (p *mockProvider) GetName() string { return "mock" }
func (p *mockProvider) GetAuthorizationURL(clientID, redirectURI, scope, state string) string {
	return ""
}
func (p *mockProvider) GetAuthorizationURLWithPKCE(clientID, redirectURI, scope, state, codeChallenge string) string {
	return ""
}
func (p *mockProvider) ExchangeCodeForToken(ctx context.Context, code, clientID, clientSecret, redirectURI string) (*oauth2.Token, error) {
	return &oauth2.Token{AccessToken: "mock_token"}, nil
}
func (p *mockProvider) GetUserInfo(ctx context.Context, accessToken string) (*providers.UserInfo, error) {
	return &providers.UserInfo{
		ID:    "user123",
		Email: p.email,
	}, nil
}
func (p *mockProvider) RefreshToken(ctx context.Context, refreshToken, clientID, clientSecret string) (*oauth2.Token, error) {
	return nil, nil
}

func TestCallback_EmailWhitelist(t *testing.T) {
	encKey := []byte("0123456789abcdef0123456789abcdef")

	t.Run("AllowedEmailSuccess", func(t *testing.T) {
		store := &mockStore{
			authRequest: map[string]any{
				"redirect_uri": "http://localhost/cb",
				"client_id":    "test_client",
				"scope":        "openid",
				"state":        "test_state",
			},
		}
		provider := &mockProvider{email: "user@example.com"}
		handler := NewHandler(store, provider, encKey, "cid", "csecret", "", "prefix_", []string{"user@example.com", "admin@example.com"})

		req := httptest.NewRequest("GET", "/callback?code=mock_code&state=test_state", nil)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusFound, rec.Code)
		assert.Contains(t, rec.Header().Get("Location"), "http://localhost/cb")
	})

	t.Run("UnauthorizedEmailForbidden", func(t *testing.T) {
		store := &mockStore{
			authRequest: map[string]any{
				"redirect_uri": "http://localhost/cb",
				"client_id":    "test_client",
				"scope":        "openid",
				"state":        "test_state",
			},
		}
		provider := &mockProvider{email: "unauthorized@example.com"}
		handler := NewHandler(store, provider, encKey, "cid", "csecret", "", "prefix_", []string{"user@example.com", "admin@example.com"})

		req := httptest.NewRequest("GET", "/callback?code=mock_code&state=test_state", nil)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusForbidden, rec.Code)

		var oauthErr types.OAuthError
		err := json.Unmarshal(rec.Body.Bytes(), &oauthErr)
		assert.NoError(t, err)
		assert.Equal(t, "access_denied", oauthErr.Error)
		assert.Equal(t, "Email not authorized to access this service", oauthErr.ErrorDescription)
	})

	t.Run("EmptyWhitelistAllowsAll", func(t *testing.T) {
		store := &mockStore{
			authRequest: map[string]any{
				"redirect_uri": "http://localhost/cb",
				"client_id":    "test_client",
				"scope":        "openid email",
				"state":        "test_state",
			},
		}
		provider := &mockProvider{email: "anyone@example.com"}
		handler := NewHandler(store, provider, encKey, "cid", "csecret", "", "prefix_", nil)

		req := httptest.NewRequest("GET", "/callback?code=mock_code&state=test_state", nil)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusFound, rec.Code)
	})
}
