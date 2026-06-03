package mcpauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

var ErrUnauthenticated = errors.New("unauthenticated")

type APIKeyValidator interface {
	Validate(ctx context.Context, raw string) (userID, keyID, tier string, err error)
}

type OAuthVerifier interface {
	Verify(ctx context.Context, jwt string) (Identity, error)
}

type Config struct {
	APIKey              APIKeyValidator
	OAuth               OAuthVerifier
	ResourceMetadataURL string
}

func New(cfg Config) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tok := bearerToken(r)
			if tok == "" {
				write401(w, cfg.ResourceMetadataURL, "missing or malformed Authorization header", "")
				return
			}
			id, err := dispatch(r.Context(), cfg, tok)
			if err != nil {
				write401(w, cfg.ResourceMetadataURL, "invalid or expired token", "invalid_token")
				return
			}
			next.ServeHTTP(w, r.WithContext(With(r.Context(), id)))
		})
	}
}

func bearerToken(r *http.Request) string {
	a := r.Header.Get("Authorization")
	const p = "Bearer "
	if !strings.HasPrefix(a, p) {
		return ""
	}
	return strings.TrimSpace(a[len(p):])
}

// dispatch routes by token prefix to avoid double DB hits: API keys start
// with bscraper_, anything else is treated as a JWT for OAuth verification.
func dispatch(ctx context.Context, cfg Config, tok string) (Identity, error) {
	if strings.HasPrefix(tok, "bscraper_") && cfg.APIKey != nil {
		uid, kid, tier, err := cfg.APIKey.Validate(ctx, tok)
		if err != nil {
			return Identity{}, err
		}
		return Identity{UserID: uid, APIKeyID: kid, Tier: tier, Method: MethodAPIKey}, nil
	}
	if cfg.OAuth != nil {
		return cfg.OAuth.Verify(ctx, tok)
	}
	return Identity{}, ErrUnauthenticated
}

func write401(w http.ResponseWriter, prm, desc, code string) {
	parts := []string{`Bearer realm="mcp"`}
	if code != "" {
		parts = append(parts, fmt.Sprintf(`error=%q`, code), fmt.Sprintf(`error_description=%q`, desc))
	}
	if prm != "" {
		parts = append(parts, fmt.Sprintf(`resource_metadata=%q`, prm))
	}
	w.Header().Set("WWW-Authenticate", strings.Join(parts, ", "))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
}
