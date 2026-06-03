-- 000041_mcp_audit.up.sql

-- Append-only audit log of every MCP tool call.
-- Required for: GDPR DSAR responses, SOC2 evidence, fraud/abuse investigation,
-- frontend "recent activity" view, post-launch user-survey questions.
-- We persist HASHES (SHA-256 hex) of arg and result bodies, not plaintext.
-- This is intentional: scraped business contact data is PII; we cannot risk
-- it leaking into application logs.
CREATE TABLE mcp_tool_call_audit (
    id              BIGSERIAL PRIMARY KEY,
    request_id      UUID NOT NULL,                                 -- correlates with slog request_id
    user_id         TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    -- For OAuth: the Clerk-issued JWT's `azp` claim (authorized party = OAuth client).
    -- For API key: NULL.
    oauth_client_id TEXT,
    -- For API key auth: the api_keys.id; NULL for OAuth.
    api_key_id      UUID,
    auth_method     TEXT NOT NULL CHECK (auth_method IN ('api_key','oauth')),
    tool_name       TEXT NOT NULL,
    args_hash       TEXT NOT NULL,                                 -- sha256 hex of canonical-JSON args
    result_hash     TEXT,                                          -- sha256 hex of result body; NULL on error
    error_code      TEXT,                                          -- structured error code or NULL
    latency_ms      INTEGER NOT NULL,
    credits_consumed NUMERIC(12,4) NOT NULL DEFAULT 0,
    client_ip       INET,
    user_agent      TEXT,
    called_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_mcp_audit_user_time ON mcp_tool_call_audit(user_id, called_at DESC);
CREATE INDEX idx_mcp_audit_request ON mcp_tool_call_audit(request_id);
