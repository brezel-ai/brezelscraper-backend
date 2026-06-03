# mcp-server

This binary serves BrezelScraper's MCP server at `/mcp` over Streamable HTTP using the
official [`github.com/modelcontextprotocol/go-sdk`](https://github.com/modelcontextprotocol/go-sdk).
It runs separately from the existing web API and currently exposes a single `ping`
tool plus a plain `/health` JSON endpoint.

## Prerequisites

- Go 1.26+
- Postgres reachable via DSN (not yet consumed by this binary, but required by the
  wider BrezelScraper backend; future tools will share that pool)

## Environment variables

| Variable          | Default  | Purpose                                  |
| ----------------- | -------- | ---------------------------------------- |
| `MCP_LISTEN_ADDR` | `:3001`  | Listen address for HTTP server           |

In local dev prefer `MCP_LISTEN_ADDR=127.0.0.1:3001` to avoid LAN exposure.

## Run locally

```bash
go run ./cmd/mcp-server
curl http://localhost:3001/health   # {"status":"ok"}
```

## Test with MCP Inspector

```bash
npx -y @modelcontextprotocol/inspector
```

In the Inspector UI:

- Transport: `Streamable HTTP`
- URL: `http://localhost:3001/mcp`
- Click **Connect** → **Tools** → `ping` → **Call**. You should see `pong` in the
  result content.

## Test with curl (two-shot JSON-RPC)

Streamable HTTP requires an `initialize` handshake before any `tools/*` call. The
session id comes back as the `Mcp-Session-Id` response header — reuse it on every
subsequent request.

```bash
# 1. Initialize and capture the session id
SESSION=$(curl -sD - http://localhost:3001/mcp -o /dev/null \
  -X POST \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -H 'MCP-Protocol-Version: 2025-11-25' \
  -d '{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"curl","version":"1"}}}' \
  | awk 'tolower($1)=="mcp-session-id:" {print $2}' | tr -d '\r\n')

# 2. List tools
curl -s http://localhost:3001/mcp \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -H 'MCP-Protocol-Version: 2025-11-25' \
  -H "Mcp-Session-Id: $SESSION" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'

# 3. Call ping
curl -s http://localhost:3001/mcp \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -H 'MCP-Protocol-Version: 2025-11-25' \
  -H "Mcp-Session-Id: $SESSION" \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"ping","arguments":{}}}'
```

## Gotchas

- The MCP spec requires the `MCP-Protocol-Version: 2025-11-25` header on every
  request after `initialize`.
- The Streamable HTTP transport returns responses as `text/event-stream`, so curl
  output is prefixed with `event: message\ndata: ...`.
- Bind to `127.0.0.1` / `localhost`, not `0.0.0.0`, in dev to avoid LAN exposure.
- Run `go test ./cmd/mcp-server/...` to exercise the health and ping unit tests.
