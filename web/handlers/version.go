package handlers

import (
	"net/http"
)

// VersionResponse contains build metadata and runtime information.
// Fields are carefully selected to balance debugging utility with security.
// Excluded: full git_commit (source targeting), go_version (CVE exploits),
// build_date (reveals deploy cadence and patch freshness to attackers).
type VersionResponse struct {
	Version        string `json:"version"`
	GitCommitShort string `json:"git_commit_short"`
	Environment    string `json:"environment"`
}

// VersionHandler handles version information requests.
type VersionHandler struct {
	Deps Dependencies
}

// NewVersionHandler creates a new version handler with injected dependencies.
func NewVersionHandler(deps Dependencies) *VersionHandler {
	return &VersionHandler{Deps: deps}
}

// GetVersion returns build metadata as JSON.
// This endpoint does not require authentication.
// Exposes: version (clean semver), git_commit_short (7 chars), environment.
// Excludes: full git_commit, go_version, build_date (OPSEC — see VersionResponse).
func (h *VersionHandler) GetVersion(w http.ResponseWriter, r *http.Request) {
	v := cleanVersion(h.Deps.Version)
	if v == "" {
		v = "dev"
	}

	gitCommit := h.Deps.GitCommit
	if len(gitCommit) > 7 {
		gitCommit = gitCommit[:7]
	}

	response := VersionResponse{
		Version:        v,
		GitCommitShort: gitCommit,
		Environment:    h.Deps.Environment.String(),
	}

	renderJSON(w, http.StatusOK, response)
}

// cleanVersion extracts a clean semver string from a build version tag.
// Strips the leading "v" prefix and any prerelease/metadata suffix after
// the first hyphen (e.g. branch name or commit hash appended by CI).
//
//	"v0.1.0-develop-abc1234" -> "0.1.0"
//	"0.1.0-dev"              -> "0.1.0"
//	"0.1.0"                  -> "0.1.0"
//	"dev"                    -> "dev"
//	""                       -> ""
func cleanVersion(raw string) string {
	v := raw
	if len(v) > 0 && v[0] == 'v' {
		v = v[1:]
	}
	for i, c := range v {
		if c == '-' {
			return v[:i]
		}
	}
	return v
}
