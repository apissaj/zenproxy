# zenproxy

[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Release](https://img.shields.io/github/v/release/hafizhmuzani/zenproxy?color=blue)](https://github.com/hafizhmuzani/zenproxy/releases)
[![CI](https://img.shields.io/github/actions/workflow/status/hafizhmuzani/zenproxy/release.yml?label=build)](https://github.com/hafizhmuzani/zenproxy/actions)
[![Go Report Card](https://goreportcard.com/badge/github.com/hafizhmuzani/zenproxy)](https://goreportcard.com/report/github.com/hafizhmuzani/zenproxy)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg)](CONTRIBUTING.md)

**Single-binary gateway that turns [OpenCode Zen](https://opencode.ai) into OpenAI & Anthropic-compatible APIs** — with multi-key failover, automatic rate-limit retry, per-key budgets, and a live web dashboard.

Built from scratch in Go. **Zero runtime dependencies.** One binary, any platform.

> ⚠️ Not affiliated with OpenCode or OpenAI. Use only where the upstream terms of service permit.

---

## ✨ Highlights

| | |
|---|---|
| 🚀 **Zero-dependency** | Pure Go stdlib — single ~4 MB binary, no Node, no Python, no Docker required |
| 🔀 **Multi-key failover** | Pool many upstream keys; auto-rotate on `429`/`402` with cooldown & recovery |
| ♻️ **Auto-retry** | Rate-limited? Retries with exponential backoff + fresh session — up to 3 attempts |
| 🎯 **Session rotation** | New `x-opencode-session` per request — dodges session-keyed free-tier limits |
| 🆓 **Free tier ready** | No account needed for `-free` models — `Bearer public` works out of the box |
| 🔐 **API key management** | SHA-256 hashed keys, model allowlists, budget caps, hot-reload |
| 💰 **Usage & cost** | Per-key token tracking with USD cost estimation, persisted to JSON |
| 📊 **Live dashboard** | Dark-theme web UI: rate windows, usage, costs, model catalog, pool status |
| 📜 **OpenAPI spec** | `openapi.yaml` for Postman / Insomnia / client generation |
| 🌐 **Multi-protocol** | OpenAI Chat Completions, Responses, Anthropic Messages, SSE streaming |

---

## 🏗 Architecture

```
┌─────────────┐   ┌──────────────┐   ┌──────────────┐   ┌──────────────────────┐
│  Your Apps  │──▶│    9Router   │──▶│   zenproxy   │──▶│   OpenCode Zen       │
│  (CLI, IDE, │   │  (optional)  │   │  :8020       │   │   /zen/v1/...        │
│   scripts)  │   └──────────────┘   │              │   │   /zen/go/v1/...     │
└─────────────┘                      │  ┌────────┐  │   └──────────────────────┘
                                     │  │ Key    │  │
                                     │  │ Pool   │──┼──▶ sk-account-1
                                     │  └────────┘  │──┼──▶ sk-account-2
                                     │              │  └──▶ sk-account-3 ...
                                     └──────────────┘
```

Requests come in OpenAI/Anthropic format → zenproxy maps them to OpenCode Zen calls → streams responses back as valid SSE.

---

## 🚀 Quick Start

### 1. Build

```bash
git clone https://github.com/hafizhmuzani/zenproxy.git
cd zenproxy
cp config.example.json config.json
go build -o zenproxy .
```

### 2. Run

```bash
./zenproxy -port 8020 -config config.json
```

### 3. Verify

```bash
curl http://127.0.0.1:8020/health       # {"status":"ok"}
curl http://127.0.0.1:8020/v1/models    # model catalog
```

### 4. Chat — free tier (no key, no account)

```bash
curl http://127.0.0.1:8020/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"mimo-v2.5","messages":[{"role":"user","content":"hello"}],"stream":false}'
```

### 5. Chat — streaming (SSE)

```bash
curl -N http://127.0.0.1:8020/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"count 1 to 3"}],"stream":true}'
```

### 6. Anthropic Messages (Claude Code compatible)

```bash
curl http://127.0.0.1:8020/v1/messages \
  -H "Content-Type: application/json" \
  -H "x-api-key: any" \
  -d '{"model":"mimo-v2.5","max_tokens":1024,"messages":[{"role":"user","content":"hello"}]}'
```

---

## 🔑 Multi-Key Failover (Upstream Pool)

Struggle with one account hitting usage limits? Throw **all your keys** into the pool — zenproxy rotates and fails over automatically.

```json
{
  "upstream_pool": {
    "enabled": true,
    "keys": [
      "sk-account-1...",
      "sk-account-2...",
      "sk-account-3..."
    ],
    "cooldown_secs": 60
  }
}
```

**How it works:**

1. Requests round-robin across all keys in the pool
2. A key that returns `429` (rate limit) or `402` (payment required) is marked **exhausted**
3. The next request automatically tries the **next healthy key**
4. After `cooldown_secs`, the exhausted key recovers and rejoins rotation

**No client changes needed** — the pool is transparent. Watch it live on the dashboard.

> 💡 **Free tier doesn't need a key at all.** `-free` models (e.g. `deepseek-v4-flash-free`) run with `Bearer public` — no account required. The pool is for paid accounts or when you want redundancy.

---

## ♻️ Automatic Retry & Session Rotation

Getting `429` from upstream? zenproxy handles it:

| Layer | Behavior |
|---|---|
| **Session rotation** | Fresh `x-opencode-session` + `x-opencode-request` per request |
| **Retry** | On `429`: wait `2s → 4s` (exponential backoff, max 15s), retry with a **fresh session** |
| **Attempts** | Up to 3 attempts per request before returning the upstream error |
| **Failover** | If a pool key is exhausted, the next key is tried immediately |

This combination means transient free-tier throttling is usually invisible to your clients.

---

## 📦 Configuration

All configuration lives in `config.json` (see [`config.example.json`](config.example.json)).

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
  "force_disable_thinking": false,

  "rate_limit": {
    "enabled": true,
    "requests_per_minute": 200,
    "tokens_per_minute": 500000,
    "exempt_keys": []
  },

  "usage": {
    "enabled": true,
    "persist_path": "usage.json",
    "max_keys": 1000,
    "cost_per_1k_input_tokens": 0.001,
    "cost_per_1k_output_tokens": 0.002
  },

  "key_auth": {
    "enabled": true,
    "keys_path": "keys.json",
    "allow_public": true
  },

  "dashboard_auth": {
    "enabled": true,
    "token": "change-me"
  },

  "upstream_pool": {
    "enabled": false,
    "keys": [],
    "cooldown_secs": 60
  }
}
```

### Config reference

| Key | Default | Description |
|---|---|---|
| `model_alias` | — | Friendly names mapped to upstream model IDs |
| `reasoning_effort_map` | — | `minimal/medium/high` → upstream reasoning effort |
| `force_disable_thinking` | `false` | Force `thinking: disabled` for all requests |
| `rate_limit.enabled` | `false` | Enable per-key rate limiting |
| `rate_limit.requests_per_minute` | `60` | Max requests per key per 60s window |
| `rate_limit.tokens_per_minute` | `500000` | Max tokens per key per 60s window |
| `rate_limit.exempt_keys` | `[]` | Keys exempt from limits |
| `usage.enabled` | `false` | Enable per-key usage & cost tracking |
| `usage.persist_path` | `usage.json` | JSON file for persisted usage |
| `usage.cost_per_1k_input_tokens` | `0.001` | Input cost per 1k tokens (USD) |
| `usage.cost_per_1k_output_tokens` | `0.002` | Output cost per 1k tokens (USD) |
| `key_auth.enabled` | `false` | Enable managed API key authentication |
| `key_auth.keys_path` | `keys.json` | JSON file storing key hashes |
| `key_auth.allow_public` | `true` | Allow unauthenticated public tier |
| `dashboard_auth.enabled` | `false` | Protect `/dashboard` with a token |
| `dashboard_auth.token` | — | Admin token for the dashboard |
| `upstream_pool.enabled` | `false` | Enable multi-key failover pool |
| `upstream_pool.keys` | `[]` | Upstream OpenCode tokens (`sk-...`) |
| `upstream_pool.cooldown_secs` | `60` | Seconds before an exhausted key recovers |

### CLI flags

| Flag | Default | Description |
|---|---|---|
| `-port` | `8000` | Listen port |
| `-config` | `config.json` | Config file path |
| `-log-level` | `info` | `debug` / `info` / `warn` / `error` |
| `-version` | — | Print version and exit |

---

## 🔐 API Key Management

Create, list, and revoke keys with the CLI (server must have `key_auth.enabled`):

```bash
# create a key with a model allowlist and monthly budget
./zenproxy key create alice --allow mimo-v2.5,deepseek-v4-flash --budget 5.0

# create a key with custom rate limits
./zenproxy key create bob --rpm 30 --tpm 50000

# list all keys (name, status, budget, spend, models)
./zenproxy key list

# revoke a key (hot-reloads within ~10s, no restart needed)
./zenproxy key revoke alice
```

Keys are stored as **SHA-256 hashes only** — the raw `zp_...` key is shown once at creation. Per-key model allowlists return `403 model_not_allowed`; budget caps return `403 budget_exceeded`.

---

## 📊 Dashboard

Open `http://127.0.0.1:8020/dashboard` (protect it with `dashboard_auth.token`):

| Panel | Shows |
|---|---|
| **Rate limiting** | Status + configured RPM/TPM limits |
| **Usage tracking** | Total requests & tokens |
| **Total cost** | Estimated spend across all keys |
| **Per-key table** | Requests, tokens, cost, last used, live rate-limit window |
| **Upstream pool** | Each key's status (`ready`/`cooldown`), uses, errors, last error |
| **Model catalog** | All models with `free` / `Go` / `alias` badges |

Auto-refreshes every 5 seconds. Raw JSON at `/dashboard/data`.

---

## 🔌 API Reference

| Endpoint | Protocol | Streaming |
|---|---|---|
| `POST /v1/chat/completions` | OpenAI | ✅ SSE |
| `POST /v1/responses` | OpenAI Responses | ✅ |
| `POST /v1/messages` | Anthropic | ✅ |
| `GET /v1/models` | OpenAI | — |
| `GET /health` | — | — |
| `GET /dashboard` | Web UI | — |
| `GET /dashboard/data` | JSON | — |

Full spec: [`openapi.yaml`](openapi.yaml) — import into Postman/Insomnia.

### Authentication modes

| `Authorization` header | Behavior |
|---|---|
| *(none)* / `Bearer public` / `no-key-required` | Free public tier, `-free` models only |
| `Bearer <sk-...>` | Auto tier — Go-only models route to Go |
| `Bearer zen:<sk-...>` | Force paid Zen catalog |
| `Bearer go:<sk-...>` | Force Go subscription catalog |
| `Bearer zp_...` (managed) | Managed key — allowlist + budget enforced |

Anthropic-style `sk-ant-*` keys are **rejected and never forwarded upstream**.

---

## 🛠 Build & Release

```bash
make build              # local binary
make test               # unit tests (35 tests)
make vet                # static checks
make release-snapshot   # cross-compile dist/ for all platforms
```

Prebuilt binaries for **Linux, macOS, Windows and FreeBSD** are attached to every [GitHub release](https://github.com/hafizhmuzani/zenproxy/releases).

### Docker

```bash
docker build -t zenproxy .
docker run --rm -p 8000:8000 -v "$PWD/config.json:/app/config.json" zenproxy
```

---

## ❓ FAQ

**Do I need an OpenCode account for free models?**
No. `-free` models run with `Bearer public` — no account, no key, no signup.

**Why am I getting 429?**
Either zenproxy's own rate limiter (`rate_limit.requests_per_minute`) or upstream throttling. Upstream `429`s auto-retry with fresh sessions; zenproxy's own limiter returns `429` with `Retry-After`.

**Can I use multiple accounts?**
Yes — for paid accounts, put all your keys in `upstream_pool.keys` and zenproxy fails over automatically.

**Is this safe for production?**
It's a single Go binary with no external calls beyond OpenCode Zen. Bind it to localhost or put it behind a reverse proxy with TLS.

---

## 📄 License

MIT — see [LICENSE](LICENSE).

Built by [Hafizh Muzani](https://github.com/hafizhmuzani) with ♥ and Go.
