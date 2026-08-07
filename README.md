# zenproxy

[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Release](https://img.shields.io/github/v/release/hafizhmuzani/zenproxy?color=blue)](https://github.com/hafizhmuzani/zenproxy/releases)
[![CI](https://img.shields.io/github/actions/workflow/status/hafizhmuzani/zenproxy/release.yml?label=build)](https://github.com/hafizhmuzani/zenproxy/actions)

Lightweight, single-binary HTTP proxy that exposes [OpenCode Zen](https://opencode.ai) upstreams as OpenAI-compatible APIs.

`zenproxy` translates OpenAI Chat Completions requests into OpenCode Zen calls, with model aliasing, reasoning-effort mapping, and the same session emulation the OpenCode CLI uses. Written from scratch in Go — no runtime dependencies, cross-compiled binaries for every major platform.

> This project is not affiliated with OpenCode or OpenAI. Use it only in environments where the upstream terms of service permit. Free-tier Zen models are rate-limited by the upstream.

## Features

- **OpenAI-compatible endpoints**: `/v1/chat/completions` (non-stream + SSE stream), `/v1/responses`, `/v1/models`
- **Anthropic Messages endpoint**: `/v1/messages` (non-stream + SSE stream, OpenAI-format chunks)
- **Free-tier routing**: no key, `Bearer public`, or placeholder keys → free `-free` Zen models
- **Paid-tier routing**: `Bearer <sk-...>` auto-detects Go-only models; `zen:` and `go:` prefixes force a tier
- **Model aliases**: expose friendly names (`mimo-v2.5`) mapped to upstream IDs (`mimo-v2.5-free`)
- **Reasoning-effort mapping**: `minimal/medium/high` → upstream values; optional `force_disable_thinking`
- **OpenCode session emulation**: `x-opencode-session` / `x-opencode-project` headers, auto-refreshed
- **Auto catalog refresh**: free + Go catalogs reloaded every 15 minutes
- **Per-key rate limiting**: sliding-window RPM + TPM limits with `Retry-After` (429), exempt keys
- **Usage & cost tracking**: per-key token usage with cost estimation, persisted to JSON
- **Web dashboard**: `/dashboard` — live rate-limit windows, per-key usage, tokens, cost, and full model catalog (dark theme, auto-refresh)
- **API key management**: create/revoke keys via CLI with SHA-256 hashed storage, per-key model allowlists and budget caps
- **Dashboard auth**: protect `/dashboard` with an admin token
- **OpenAPI spec**: `openapi.yaml` — import into Postman/Insomnia or generate clients
- **Structured logging**: JSON-adjacent slog output with `X-Request-Id` correlation

## Quick start

```bash
git clone https://github.com/hafizhmuzani/zenproxy.git
cd zenproxy
cp config.example.json config.json
go build -o zenproxy .
./zenproxy -port 8000 -config config.json
```

Health check and model list:

```bash
curl http://127.0.0.1:8000/health
curl http://127.0.0.1:8000/v1/models
```

Chat completion (free tier — no key needed):

```bash
curl http://127.0.0.1:8000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"mimo-v2.5","messages":[{"role":"user","content":"hello"}],"stream":false}'
```

Streaming:

```bash
curl -N http://127.0.0.1:8000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"count 1 to 3"}],"stream":true}'
```

OpenAI Responses API:

```bash
curl http://127.0.0.1:8000/v1/responses \
  -H "Content-Type: application/json" \
  -d '{"model":"mimo-v2.5","input":"hello"}'
```

Anthropic Messages API (Claude Code compatible):

```bash
curl http://127.0.0.1:8000/v1/messages \
  -H "Content-Type: application/json" \
  -H "x-api-key: public" \
  -d '{"model":"mimo-v2.5","max_tokens":1024,"messages":[{"role":"user","content":"hello"}]}'
```

## Configuration

All configuration lives in `config.json` (see `config.example.json`):

| Key | Description |
|---|---|
| `model_alias` | Friendly names mapped to upstream model IDs |
| `reasoning_effort_map` | Map `minimal/medium/high` → upstream reasoning effort |
| `force_disable_thinking` | Force `thinking: disabled` for all requests |
| `rate_limit.enabled` | Enable per-key rate limiting |
| `rate_limit.requests_per_minute` | Max requests per key per 60s window |
| `rate_limit.tokens_per_minute` | Max tokens per key per 60s window |
| `rate_limit.exempt_keys` | Keys exempt from limits (e.g. `["admin"]`) |
| `usage.enabled` | Enable per-key usage & cost tracking |
| `usage.persist_path` | JSON file to persist usage (`usage.json`) |
| `usage.cost_per_1k_input_tokens` | Cost per 1k input tokens (USD) |
| `usage.cost_per_1k_output_tokens` | Cost per 1k output tokens (USD) |
| `key_auth.enabled` | Enable managed API key authentication |
| `key_auth.keys_path` | JSON file storing key hashes (`keys.json`) |
| `key_auth.allow_public` | Allow unauthenticated public tier (free models) |
| `dashboard_auth.enabled` | Protect `/dashboard` with a token |
| `dashboard_auth.token` | Admin token for the dashboard |

## API Key Management

Create, list, and revoke keys with the CLI (server must have `key_auth.enabled`):

```bash
# create a key with a model allowlist and monthly budget
./zenproxy key create alice --allow mimo-v2.5,deepseek-v4-flash --budget 5.0

# create a key with custom rate limits
./zenproxy key create bob --rpm 30 --tpm 50000

# list all keys (name, status, budget, spend, models)
./zenproxy key list

# revoke a key (takes effect within ~10s, hot-reload)
./zenproxy key revoke alice
```

Keys are stored as **SHA-256 hashes** only — the raw `zp_...` key is shown once at creation. Per-key model allowlists return `403 model_not_allowed`; budget caps return `403 budget_exceeded` when spend reaches the limit.

## API Reference

Full OpenAPI spec: [`openapi.yaml`](openapi.yaml) — import into Postman/Insomnia or use with code generators.

## Dashboard

Open `http://127.0.0.1:8000/dashboard` for a live overview:

- **Rate limiting** status and configured limits
- **Usage tracking** totals (requests, tokens)
- **Total cost** across all keys
- Per-key table: requests, tokens, cost, last used, live rate-limit window
- **Model catalog**: all models from free + Go catalogs, with `free` / `Go` / `alias` badges and upstream resolution

The dashboard auto-refreshes every 5 seconds. Raw JSON is available at `/dashboard/data`.

## Authentication modes

| `Authorization` header | Behavior |
|---|---|
| *(none)*, `Bearer public`, `no-key-required` | Free public tier, `-free` models only |
| `Bearer <sk-...>` | Auto tier — models exclusive to the Go catalog route to Go |
| `Bearer zen:<sk-...>` | Force paid Zen catalog |
| `Bearer go:<sk-...>` | Force Go subscription catalog |

Anthropic-style `sk-ant-*` keys are rejected and never forwarded upstream.

## Configuration

`config.json` (see `config.example.json`):

```json
{
  "model_alias": {
    "mimo-v2.5": "mimo-v2.5-free"
  },
  "reasoning_effort_map": {
    "minimal": "low",
    "medium": "medium",
    "high": "high"
  },
  "force_disable_thinking": false
}
```

| Flag | Default | Description |
|---|---|---|
| `-port` | `8000` | Listen port |
| `-config` | `config.json` | Config file path |
| `-log-level` | `info` | `debug` / `info` / `warn` / `error` |
| `-version` | — | Print version and exit |

## Build

```bash
make build    # local binary
make test     # unit tests
make vet      # static checks
make release-snapshot  # cross-compile dist/
```

Prebuilt binaries for Linux, macOS, Windows and FreeBSD are attached to every [GitHub release](https://github.com/hafizhmuzani/zenproxy/releases).

## Docker

```bash
docker build -t zenproxy .
docker run --rm -p 8000:8000 -v "$PWD/config.json:/app/config.json" zenproxy
```

## License

MIT — see [LICENSE](LICENSE).
