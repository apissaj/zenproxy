<div align="center">

# ⚡ zenproxy

**OpenAI & Anthropic-compatible gateway for [OpenCode Zen](https://opencode.ai)**

Drop-in proxy with multi-key failover, automatic rate-limit retry, per-key budgets, and a live web dashboard — wrapped in a single Go binary with zero dependencies.

[![Go](https://img.shields.io/badge/Go-%3E%3D1.22-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Release](https://img.shields.io/github/v/release/apissaj/zenproxy?color=blue)](https://github.com/apissaj/zenproxy/releases)
[![CI](https://img.shields.io/github/actions/workflow/status/apissaj/zenproxy/release.yml?label=build)](https://github.com/apissaj/zenproxy/actions)
[![Tests](https://img.shields.io/badge/tests-35%20passed-green.svg)](.)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg)](.)
[![Go Report Card](https://goreportcard.com/badge/github.com/apissaj/zenproxy)](https://goreportcard.com/report/github.com/apissaj/zenproxy)

</div>

---

## ✨ Features

| Feature | Description |
|---|---|
| 🚀 **Zero dependencies** | Pure Go stdlib — single ~4 MB binary, no Node, no Python, no Docker required |
| 🔀 **Multi-key failover** | Pool many upstream keys; auto-rotate on `429`/`402` with cooldown & auto-recovery |
| ♻️ **Auto-retry** | Rate-limited? Retries with exponential backoff + fresh session — up to 3 attempts |
| 🎯 **Session rotation** | New `x-opencode-session` per request — sidesteps session-keyed free-tier limits |
| 🌐 **DNS-failure resilient** | Retries on transport errors (DNS lookup failure, conn reset, timeout) — robust on flaky networks |
| 🆓 **Free tier ready** | No account needed for `-free` models — `Bearer public` works out of the box |
| 🔐 **API key management** | SHA-256 hashed keys, per-key model allowlists and monthly budget caps |
| 💰 **Usage & cost** | Per-key token tracking with USD cost estimation, persisted to JSON |
| 📊 **Live dashboard** | Dark-theme web UI: rate windows, usage, costs, pool status, model catalog |
| 📜 **OpenAPI spec** | `openapi.yaml` for Postman / Insomnia / client generation |
| 🤖 **Multi-protocol** | OpenAI Chat Completions, Responses, Anthropic Messages — SSE streaming everywhere |
| 🪟 **Windows autostart** | Hidden VBS launcher for boot-time startup (optional) |

---

## 🏗 Architecture

```
┌─────────────┐   ┌──────────────┐   ┌──────────────────┐   ┌──────────────────────┐
│  Your Apps  │──▶│    9Router   │──▶│     zenproxy     │──▶│     OpenCode Zen     │
│  (CLI, IDE, │   │  (optional)  │   │      :8020       │   │     /zen/v1/...      │
│   scripts)  │   └──────────────┘   │                  │   │     /zen/go/v1/...   │
└─────────────┘                      │   ┌───────────┐   │   └──────────────────────┘
                                     │   │   Key     │   │
                                     │   │   Pool    │──▶┬──▶ sk-account-1
                                     │   │           │  ├──▶ sk-account-2
                                     │   └───────────┘  └──▶ sk-account-3 ...
                                     │                  │
                                     │   ┌───────────┐   │
                                     │   │  Session  │──▶│──▶ Fresh x-opencode-session
                                     │   │  Rotator  │   │    per request
                                     │   └───────────┘   │
                                     └──────────────────┘
```

---

## 🚀 Quick Start

### 1. Build from source

```bash
git clone https://github.com/apissaj/zenproxy.git
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

## 🌐 Proxy Pool — Bypass Per-IP Free-Tier Limits

OpenCode Zen's free tier (`-free` models) limits requests per egress IP. If you hit `FreeUsageLimitError` and want to continue without waiting for the cooldown, route requests through rotating HTTP/SOCKS proxies:

```json
{
  "proxy_pool": {
    "enabled": true,
    "proxies": [
      "http://proxy1.example.com:8080",
      "http://user:pass@proxy2.example.com:3128",
      "socks5://proxy3.example.com:1080"
    ],
    "cooldown_secs": 60
  }
}
```

**How it works:**

1. Each request picks the next healthy proxy (round-robin)
2. A proxy that returns `429`/`402`/5xx or transport errors is marked **exhausted**
3. The next request automatically uses the **next healthy proxy**
4. After `cooldown_secs`, the exhausted proxy recovers and rejoins rotation
5. Successful requests clear the proxy's cooldown immediately

**Zero dependencies**: only standard `http.Transport.Proxy` for `http(s)://` schemes. For `socks5://`, configure a local HTTP-to-SOCKS wrapper or use `http://` proxies (most common for proxy rotation services).

> 💡 **Combine with key pool** — when both are enabled, you get key × proxy rotation matrix. Free tier doesn't need keys but proxy rotation helps; paid tier benefits from key pool for cost/account isolation.

---

## 🔑 Multi-Key Failover (Upstream Pool)

Hit usage limits on a single account? Throw **all your keys** into the pool — zenproxy rotates and fails over automatically.

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
| **Transport retry** | DNS failures, connection resets, and timeouts retry with backoff |
| **HTTP retry** | On `429`/`402`/5xx: wait `2s → 4s` (exponential backoff, max 15s) |
| **Attempts** | Up to 3 attempts per request before returning the upstream error |
| **Failover** | If a pool key is exhausted, the next key is tried immediately |

This combination means transient free-tier throttling and flaky-network hiccups are usually invisible to your clients.

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
    "tokens_per_minute": 2000000,
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
| `rate_limit.tokens_per_minute` | `500000` | Max tokens per key per 60s window. **Raise this** for coding agents that send large prompts (100K+ tokens). Default 500K ≈ 1-2 requests; **2M** handles 10-15 large requests/min. |
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
| `proxy_pool.enabled` | `false` | Enable HTTP/SOCKS proxy rotation |
| `proxy_pool.proxies` | `[]` | Proxy URLs (`http://user:pass@host:port`, `socks5://...`) |
| `proxy_pool.cooldown_secs` | `60` | Seconds before an exhausted proxy recovers |

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
| **Proxy pool** | Each proxy's status (`ready`/`cooldown`), uses, errors, last error |
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

Prebuilt binaries for **Linux, macOS, Windows and FreeBSD** are attached to every [GitHub release](https://github.com/apissaj/zenproxy/releases).

### Docker

```bash
docker build -t zenproxy .
docker run --rm -p 8000:8000 -v "$PWD/config.json:/app/config.json" zenproxy
```

### Windows autostart (optional)

```cmd
cscript autostart.vbs
```

Registers `zenproxy.exe` to start at boot, logs to `zenproxy.log`.

---

## ❓ FAQ

**Do I need an OpenCode account for free models?**
No. `-free` models run with `Bearer public` — no account, no key, no signup.

**Why am I getting 429?**
Either zenproxy's own rate limiter (`rate_limit.requests_per_minute` / `tokens_per_minute`) or upstream throttling. Upstream `429`s auto-retry with fresh sessions; zenproxy's own limiter returns `429` with `Retry-After`. For coding agents sending 100K+ token prompts, raise `tokens_per_minute` to **2M**.

**Can I use multiple accounts?**
Yes — for paid accounts, put all your keys in `upstream_pool.keys` and zenproxy fails over automatically.

**Is this safe for production?**
It's a single Go binary with no external calls beyond OpenCode Zen. Bind it to localhost or put it behind a reverse proxy with TLS.

---

## 📄 License

MIT — see [LICENSE](LICENSE).

<div align="center">

Built by [apissaj](https://github.com/apissaj) · Built with ♥ and Go

</div>