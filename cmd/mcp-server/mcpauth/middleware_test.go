package mcpauth_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gosom/google-maps-scraper/cmd/mcp-server/mcpauth"
)

type fakeAPIKey struct{ valid map[string]string }

func (f *fakeAPIKey) Validate(_ context.Context, raw string) (userID, keyID, tier string, err error) {
	if uid, ok := f.valid[raw]; ok {
		return uid, "key-" + uid, "free", nil
	}
	return "", "", "", errors.New("bad key")
}

type fakeOAuth struct{ valid map[string]mcpauth.Identity }

func (f *fakeOAuth) Verify(_ context.Context, raw string) (mcpauth.Identity, error) {
	if id, ok := f.valid[raw]; ok {
		return id, nil
	}
	return mcpauth.Identity{}, errors.New("bad jwt")
}

func TestMiddleware_APIKey_Valid(t *testing.T) {
	mw := mcpauth.New(mcpauth.Config{
		APIKey:              &fakeAPIKey{valid: map[string]string{"bscraper_abc": "u_123"}},
		ResourceMetadataURL: "https://mcp.brezelscraper.com/.well-known/oauth-protected-resource",
	})
	var got mcpauth.Identity
	h := mw(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got, _ = mcpauth.From(r.Context())
	}))
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/mcp", nil)
	req.Header.Set("Authorization", "Bearer bscraper_abc")
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "u_123", got.UserID)
	assert.Equal(t, mcpauth.MethodAPIKey, got.Method)
}

func TestMiddleware_OAuth_Valid(t *testing.T) {
	mw := mcpauth.New(mcpauth.Config{
		OAuth: &fakeOAuth{valid: map[string]mcpauth.Identity{
			"eyJhbg": {UserID: "u_999", Method: mcpauth.MethodOAuth, ClientID: "claude-ai"},
		}},
		ResourceMetadataURL: "https://mcp.brezelscraper.com/.well-known/oauth-protected-resource",
	})
	var got mcpauth.Identity
	h := mw(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got, _ = mcpauth.From(r.Context())
	}))
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/mcp", nil)
	req.Header.Set("Authorization", "Bearer eyJhbg")
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "u_999", got.UserID)
	assert.Equal(t, mcpauth.MethodOAuth, got.Method)
}

func TestMiddleware_Missing_ReturnsPRMHeader(t *testing.T) {
	mw := mcpauth.New(mcpauth.Config{
		APIKey:              &fakeAPIKey{},
		OAuth:               &fakeOAuth{},
		ResourceMetadataURL: "https://mcp.brezelscraper.com/.well-known/oauth-protected-resource",
	})
	rr := httptest.NewRecorder()
	mw(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { t.Fatal("unreachable") })).ServeHTTP(rr, httptest.NewRequest("POST", "/mcp", nil))
	require.Equal(t, http.StatusUnauthorized, rr.Code)
	wa := rr.Header().Get("WWW-Authenticate")
	assert.Contains(t, wa, `Bearer`)
	assert.Contains(t, wa, `resource_metadata=`)
	assert.True(t, strings.Contains(wa, "https://mcp.brezelscraper.com"))
}

func TestMiddleware_InvalidAPIKey_Returns401(t *testing.T) {
	mw := mcpauth.New(mcpauth.Config{
		APIKey:              &fakeAPIKey{valid: map[string]string{"bscraper_ok": "u_1"}},
		ResourceMetadataURL: "https://mcp.brezelscraper.com/.well-known/oauth-protected-resource",
	})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/mcp", nil)
	req.Header.Set("Authorization", "Bearer bscraper_WRONG")
	mw(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { t.Fatal("unreachable") })).ServeHTTP(rr, req)
	require.Equal(t, http.StatusUnauthorized, rr.Code)
}
