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
