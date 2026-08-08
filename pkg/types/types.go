package types

// Config holds all configuration values for the OAuth proxy
type Config struct {
	Port                 string
	DatabaseDSN          string
	OAuthClientID        string
	OAuthClientSecret    string
	OAuthAuthorizeURL    string
	OAuthJWKSURL         string
	ScopesSupported      string
	MCPServerURL         string
	EncryptionKey        string
	Mode                 string
	RoutePrefix          string
	CookieNamePrefix     string
	MCPServerID          string
	APIKeyAuthWebhookURL string
	TrustedIssuer        string
	TrustedAudiences     string
	MCPPaths             []string
	AllowedEmails        string // Comma-separated list of allowed user emails (optional)
}
