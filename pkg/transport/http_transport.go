package transport

import (
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"

	"github.com/obot-platform/mcp-oauth-proxy/pkg/tokens"
)

// HTTPTransport implements Transport for HTTP/HTTPS upstream MCP servers
type HTTPTransport struct {
	targetURL          string
	routePrefix        string
	cookieNamePrefix   string
}

// NewHTTPTransport creates a new HTTP transport for proxying to an HTTP upstream MCP server
func NewHTTPTransport(mcpServerURL, routePrefix, cookieNamePrefix string) (*HTTPTransport, error) {
	targetURL, err := url.Parse(mcpServerURL)
	if err != nil {
		return nil, fmt.Errorf("invalid MCP server URL: %w", err)
	}

	// Validate the URL scheme
	if targetURL.Scheme != "http" && targetURL.Scheme != "https" {
		return nil, fmt.Errorf("invalid MCP server URL scheme: %s (must be http or https)", targetURL.Scheme)
	}

	return &HTTPTransport{
		targetURL:        mcpServerURL,
		routePrefix:      routePrefix,
		cookieNamePrefix: cookieNamePrefix,
	}, nil
}

// ServeHTTP proxies an HTTP request to the upstream HTTP MCP server
func (t *HTTPTransport) ServeHTTP(w http.ResponseWriter, r *http.Request, tokenInfo *tokens.TokenInfo) {
	// Build the target URL
	targetURL := t.targetURL

	// Get the path from the request and append to target URL
	path := r.URL.Path

	// For proxy mode, the path includes the route prefix
	// We need to reconstruct the upstream URL with the path
	upstreamURL := targetURL

	// Append path if it's not just the root
	if path != "" && path != "/" {
		// Remove the route prefix from the path if present
		if t.routePrefix != "" {
			// Remove the route prefix to get the path for the upstream server
			relPath := path
			if len(path) >= len(t.routePrefix) && path[:len(t.routePrefix)] == t.routePrefix {
				relPath = path[len(t.routePrefix):]
			}
			if relPath != "" && relPath != "/" {
				upstreamURL = targetURL + relPath
			}
		} else {
			upstreamURL = targetURL + path
		}
	}

	// Append query string if present
	if r.URL.RawQuery != "" {
		upstreamURL += "?" + r.URL.RawQuery
	}

	log.Printf("Proxying request: %s %s -> %s", r.Method, r.URL.Path, upstreamURL)

	// Create the reverse proxy
	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			// Set the target URL
			upstream, _ := url.Parse(upstreamURL)
			req.URL.Scheme = upstream.Scheme
			req.URL.Host = upstream.Host
			req.URL.Path = upstream.Path
			req.URL.RawQuery = upstream.RawQuery

			// Remove Authorization header - the OAuth proxy handles authentication
			req.Header.Del("Authorization")

			// Add forwarded headers from token info if available
			if tokenInfo != nil && tokenInfo.Props != nil {
				if userID, ok := tokenInfo.Props["user_id"].(string); ok {
					req.Header.Set("X-Forwarded-User", userID)
				}
				if email, ok := tokenInfo.Props["email"].(string); ok {
					req.Header.Set("X-Forwarded-Email", email)
				}
				if name, ok := tokenInfo.Props["name"].(string); ok {
					req.Header.Set("X-Forwarded-Name", name)
				}
				if accessToken, ok := tokenInfo.Props["access_token"].(string); ok {
					req.Header.Set("X-Forwarded-Access-Token", accessToken)
				}
			}

			// Preserve X-Forwarded-Host and X-Forwarded-Proto
			if rh := r.Header.Get("X-Forwarded-Host"); rh != "" {
				req.Header.Set("X-Forwarded-Host", rh)
			} else {
				req.Header.Set("X-Forwarded-Host", r.Host)
			}

			if rp := r.Header.Get("X-Forwarded-Proto"); rp != "" {
				req.Header.Set("X-Forwarded-Proto", rp)
			} else {
				scheme := "http"
				if r.TLS != nil {
					scheme = "https"
				}
				req.Header.Set("X-Forwarded-Proto", scheme)
			}
		},
		ErrorHandler: func(rw http.ResponseWriter, req *http.Request, err error) {
			log.Printf("Proxy error for %s: %v", req.URL.Path, err)
			rw.WriteHeader(http.StatusBadGateway)
		},
		ModifyResponse: func(resp *http.Response) error {
			// Rewrite Location header to use proxy host instead of downstream server host
			if location := resp.Header.Get("Location"); location != "" {
				if locationURL, err := url.Parse(location); err == nil {
					proxyHost := resp.Request.Header.Get("X-Forwarded-Host")
					if proxyHost != "" {
						downstreamURL, _ := url.Parse(t.targetURL)
						if locationURL.Host == downstreamURL.Host {
							locationURL.Scheme = resp.Request.URL.Scheme
							locationURL.Host = proxyHost
							resp.Header.Set("Location", locationURL.String())
						}
					}
				}
			}
			return nil
		},
	}

	proxy.ServeHTTP(w, r)
}