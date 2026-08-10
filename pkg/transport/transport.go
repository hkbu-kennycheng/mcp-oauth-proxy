package transport

import (
	"net/http"

	"github.com/obot-platform/mcp-oauth-proxy/pkg/tokens"
)

type Transport interface {
	// ServeHTTP handles the HTTP request and proxies it to the upstream MCP server
	// The tokenInfo contains user identity information to be passed to the upstream
	ServeHTTP(w http.ResponseWriter, r *http.Request, tokenInfo *tokens.TokenInfo)
}

// ServeHTTPFunc is an adapter to allow using functions as Transport
type ServeHTTPFunc func(w http.ResponseWriter, r *http.Request, tokenInfo *tokens.TokenInfo)

func (f ServeHTTPFunc) ServeHTTP(w http.ResponseWriter, r *http.Request, tokenInfo *tokens.TokenInfo) {
	f(w, r, tokenInfo)
}
