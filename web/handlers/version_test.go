package handlers_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gosom/google-maps-scraper/pkg/appenv"
	"github.com/gosom/google-maps-scraper/web/handlers"
)

func TestGetVersion(t *testing.T) {
	tests := []struct {
		name           string
		deps           handlers.Dependencies
		wantVersion    string
		wantEnv        string
		wantCommit     string
		wantBuildDate  string
		wantStatusCode int
	}{
		{
			name: "production environment",
			deps: handlers.Dependencies{
				Version:     "v0.1.0-develop-abc1234",
				GitCommit:   "abc1234567890abcdef1234567890abcdef123456",
				BuildDate:   "2026-05-27T14:11:00Z",
				Environment: appenv.Production,
			},
			wantVersion:    "0.1.0",
			wantEnv:        "production",
			wantCommit:     "abc1234",
			wantBuildDate:  "2026-05-27T14:11:00Z",
			wantStatusCode: http.StatusOK,
		},
		{
			name: "development environment (zero value)",
			deps: handlers.Dependencies{
				Version:   "0.1.0",
				GitCommit: "abc1234",
			},
			wantVersion:    "0.1.0",
			wantEnv:        "development",
			wantCommit:     "abc1234",
			wantStatusCode: http.StatusOK,
		},
		{
			name: "staging environment",
			deps: handlers.Dependencies{
				Version:     "0.2.0",
				Environment: appenv.Staging,
			},
			wantVersion:    "0.2.0",
			wantEnv:        "staging",
			wantStatusCode: http.StatusOK,
		},
		{
			name: "version with -dev suffix is cleaned",
			deps: handlers.Dependencies{
				Version: "0.1.0-dev",
			},
			wantVersion:    "0.1.0",
			wantEnv:        "development",
			wantStatusCode: http.StatusOK,
		},
		{
			name: "version with v prefix and branch suffix",
			deps: handlers.Dependencies{
				Version: "v0.1.0-develop-abc1234",
			},
			wantVersion:    "0.1.0",
			wantStatusCode: http.StatusOK,
		},
		{
			name: "clean semver passthrough",
			deps: handlers.Dependencies{
				Version: "1.2.3",
			},
			wantVersion:    "1.2.3",
			wantStatusCode: http.StatusOK,
		},
		{
			name:           "empty version falls back to dev",
			deps:           handlers.Dependencies{},
			wantVersion:    "dev",
			wantStatusCode: http.StatusOK,
		},
		{
			name: "git commit truncated to 7 chars",
			deps: handlers.Dependencies{
				GitCommit: "abc1234567890abcdef",
			},
			wantCommit:     "abc1234",
			wantStatusCode: http.StatusOK,
		},
		{
			name: "short git commit unchanged",
			deps: handlers.Dependencies{
				GitCommit: "abc",
			},
			wantCommit:     "abc",
			wantStatusCode: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := handlers.NewVersionHandler(tt.deps)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/version", nil)
			rr := httptest.NewRecorder()

			h.GetVersion(rr, req)

			if rr.Code != tt.wantStatusCode {
				t.Fatalf("expected status %d, got %d", tt.wantStatusCode, rr.Code)
			}

			if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("expected Content-Type application/json, got %q", ct)
			}

			body, _ := io.ReadAll(rr.Body)
			var resp handlers.VersionResponse
			if err := json.Unmarshal(body, &resp); err != nil {
				t.Fatalf("could not parse JSON body: %v\nbody: %s", err, body)
			}

			if tt.wantVersion != "" && resp.Version != tt.wantVersion {
				t.Errorf("version: expected %q, got %q", tt.wantVersion, resp.Version)
			}
			if tt.wantEnv != "" && resp.Environment != tt.wantEnv {
				t.Errorf("environment: expected %q, got %q", tt.wantEnv, resp.Environment)
			}
			if tt.wantCommit != "" && resp.GitCommitShort != tt.wantCommit {
				t.Errorf("git_commit_short: expected %q, got %q", tt.wantCommit, resp.GitCommitShort)
			}
			if tt.wantBuildDate != "" && resp.BuildDate != tt.wantBuildDate {
				t.Errorf("build_date: expected %q, got %q", tt.wantBuildDate, resp.BuildDate)
			}
		})
	}
}
