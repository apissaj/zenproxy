package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"time"
)

// dashboardData is the JSON payload served by /dashboard/data.
type dashboardData struct {
	GeneratedAt  string                `json:"generated_at"`
	Version      string                `json:"version"`
	RateLimit    *RateLimitConfig      `json:"rate_limit"`
	RateWindows  map[string]RateStatus `json:"rate_windows"`
	UsageEnabled bool                  `json:"usage_enabled"`
	Usage        map[string]KeyUsage   `json:"usage"`
	Models       []ModelEntry          `json:"models"`
	Keys         []KeyEntry            `json:"keys"`
	PoolStatus   []UpstreamKeyStatus   `json:"pool_status"`
	PoolEnabled  bool                  `json:"pool_enabled"`
}

// KeyEntry is a dashboard key row (managed key store).
type KeyEntry struct {
	Name      string   `json:"name"`
	Hash      string   `json:"hash"`
	CreatedAt string   `json:"created_at"`
	Revoked   bool     `json:"revoked"`
	ModelAllow []string `json:"model_allow,omitempty"`
	BudgetUSD float64  `json:"budget_usd,omitempty"`
	SpendUSD  float64  `json:"spend_usd,omitempty"`
}

// ModelEntry is a dashboard model row.
type ModelEntry struct {
	ID       string `json:"id"`
	IsFree   bool   `json:"is_free"`
	IsGo     bool   `json:"is_go"`
	IsAlias  bool   `json:"is_alias"`
	Upstream string `json:"upstream,omitempty"`
}

func dashboardHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(dashboardHTML))
}

func dashboardDataHandler(w http.ResponseWriter, r *http.Request) {
	data := dashboardData{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		RateWindows: map[string]RateStatus{},
		Usage:       map[string]KeyUsage{},
		Models:      collectModelEntries(),
		Keys:        collectKeyEntries(),
	}
	if upstreamPool != nil {
		data.PoolEnabled = true
		data.PoolStatus = upstreamPool.Status()
	}
	if rateLimiter != nil {
		data.RateLimit = &RateLimitConfig{
			Enabled:           rateLimiter.enabled,
			RequestsPerMinute: rateLimiter.reqLim,
			TokensPerMinute:   rateLimiter.tokLim,
		}
		data.RateWindows = rateLimiter.Snapshot()
	}
	if usageStore_ != nil {
		data.UsageEnabled = usageStore_.enabled
		data.Usage = usageStore_.Snapshot()
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

// collectModelEntries builds the dashboard model list from the live catalogs
// (free Zen + Go), flagging aliases and the upstream ID they resolve to.
func collectModelEntries() []ModelEntry {
	modelMu.RLock()
	free := make([]ModelInfo, len(modelsCache))
	copy(free, modelsCache)
	goCat := make([]ModelInfo, len(goModelsCache))
	copy(goCat, goModelsCache)
	modelMu.RUnlock()

	configMu.RLock()
	aliases := make(map[string]string, len(modelAlias))
	for a, u := range modelAlias {
		aliases[a] = u
	}
	configMu.RUnlock()

	seen := map[string]bool{}
	var out []ModelEntry
	add := func(id string, isFree, isGo bool) {
		if seen[id] {
			return
		}
		seen[id] = true
		e := ModelEntry{ID: id, IsFree: isFree, IsGo: isGo}
		if up, ok := aliases[id]; ok {
			e.IsAlias = true
			e.Upstream = up
		}
		out = append(out, e)
	}
	for _, m := range free {
		add(publicFacingID(m.ID), true, false)
	}
	for _, m := range goCat {
		pid := publicFacingID(m.ID)
		if seen[pid] {
			// already listed as free; mark as also-go
			for i := range out {
				if out[i].ID == pid {
					out[i].IsGo = true
				}
			}
			continue
		}
		add(pid, false, true)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// collectKeyEntries lists managed keys with live spend (dashboard).
func collectKeyEntries() []KeyEntry {
	if keyStore == nil {
		return nil
	}
	var out []KeyEntry
	for _, rec := range keyStore.List() {
		e := KeyEntry{
			Name:      rec.Name,
			Hash:      rec.Hash[:12] + "…",
			CreatedAt: rec.CreatedAt.Format(time.RFC3339),
			Revoked:   rec.Revoked,
			ModelAllow: rec.ModelAllow,
			BudgetUSD: rec.BudgetUSD,
			SpendUSD:  activeSpendUSD(rec),
		}
		out = append(out, e)
	}
	return out
}

// sortedKeys is a helper for stable rendering.
func sortedKeys(m map[string]KeyUsage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

const dashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>zenproxy dashboard</title>
<style>
  :root {
    --bg: #0d1117; --panel: #161b22; --border: #30363d; --text: #e6edf3;
    --muted: #8b949e; --accent: #58a6ff; --green: #3fb950; --red: #f85149;
    --yellow: #d29922; --mono: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  }
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body { background: var(--bg); color: var(--text); font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; padding: 32px 24px; }
  h1 { font-size: 20px; font-weight: 600; margin-bottom: 4px; }
  h1 .dot { color: var(--green); }
  .sub { color: var(--muted); font-size: 13px; margin-bottom: 24px; }
  .grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(280px, 1fr)); gap: 16px; margin-bottom: 24px; }
  .card { background: var(--panel); border: 1px solid var(--border); border-radius: 8px; padding: 16px; }
  .card h3 { font-size: 12px; text-transform: uppercase; letter-spacing: .05em; color: var(--muted); margin-bottom: 12px; }
  .stat { display: flex; justify-content: space-between; align-items: baseline; font-family: var(--mono); font-size: 22px; }
  .stat .label { font-size: 12px; color: var(--muted); font-family: inherit; }
  .badge { display: inline-block; padding: 2px 8px; border-radius: 10px; font-size: 11px; font-weight: 600; }
  .badge.on { background: rgba(63,185,80,.15); color: var(--green); }
  .badge.off { background: rgba(139,148,158,.15); color: var(--muted); }
  table { width: 100%; border-collapse: collapse; background: var(--panel); border: 1px solid var(--border); border-radius: 8px; overflow: hidden; font-size: 13px; }
  th { text-align: left; padding: 10px 12px; color: var(--muted); font-size: 11px; text-transform: uppercase; letter-spacing: .05em; border-bottom: 1px solid var(--border); }
  td { padding: 10px 12px; border-bottom: 1px solid var(--border); font-family: var(--mono); font-size: 12px; }
  tr:last-child td { border-bottom: none; }
  .key { max-width: 220px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .num { text-align: right; }
  .limited { color: var(--red); }
  .ok { color: var(--green); }
  .empty { color: var(--muted); padding: 20px; text-align: center; }
  .footer { margin-top: 24px; color: var(--muted); font-size: 12px; font-family: var(--mono); }
</style>
</head>
<body>
  <h1><span class="dot">●</span> zenproxy <span id="version" style="color:var(--muted);font-weight:400;font-size:14px"></span></h1>
  <div class="sub">OpenAI/Anthropic-compatible proxy · live rate limit &amp; usage per key</div>

  <div class="grid">
    <div class="card">
      <h3>Rate limiting</h3>
      <div class="stat"><span id="rl-status" class="badge">—</span></div>
      <div class="sub" id="rl-limits" style="margin-top:8px"></div>
    </div>
    <div class="card">
      <h3>Usage tracking</h3>
      <div class="stat"><span id="usage-status" class="badge">—</span></div>
      <div class="sub" id="usage-total" style="margin-top:8px"></div>
    </div>
    <div class="card">
      <h3>Total cost</h3>
      <div class="stat"><span id="total-cost" style="font-size:20px">—</span></div>
      <div class="sub" style="margin-top:8px">all keys · estimated</div>
    </div>
  </div>

  <table>
    <thead>
      <tr>
        <th>API key</th>
        <th class="num">Requests</th>
        <th class="num">Tokens</th>
        <th class="num">Cost (USD)</th>
        <th class="num">Last used</th>
        <th>Rate limit (60s)</th>
      </tr>
    </thead>
    <tbody id="rows"></tbody>
  </table>
  <div class="empty" id="empty" style="display:none">No usage yet — send a request to see it here.</div>

  <div style="margin-top:32px;display:flex;align-items:center;justify-content:space-between;flex-wrap:wrap;gap:12px">
    <h2 style="font-size:16px;font-weight:600">API Keys</h2>
    <span style="font-size:12px;color:var(--muted)">managed keys · budget &amp; allowlist</span>
  </div>
  <table style="margin-top:12px">
    <thead>
      <tr>
        <th>Name</th>
        <th>Hash</th>
        <th>Status</th>
        <th class="num">Budget</th>
        <th class="num">Spend</th>
        <th>Models</th>
      </tr>
    </thead>
    <tbody id="key-rows"></tbody>
  </table>
  <div class="empty" id="keys-empty" style="display:none">Key management disabled or no keys yet — use <code>zenproxy key create</code>.</div>

  <div style="margin-top:32px;display:flex;align-items:center;justify-content:space-between;flex-wrap:wrap;gap:12px">
    <h2 style="font-size:16px;font-weight:600">Upstream Pool</h2>
    <span style="font-size:12px;color:var(--muted)">multi-key failover · auto-rotate on 429/402</span>
  </div>
  <table style="margin-top:12px">
    <thead>
      <tr>
        <th>Alias</th>
        <th>Status</th>
        <th class="num">Cooldown until</th>
        <th class="num">Uses</th>
        <th class="num">Errors</th>
        <th>Last error</th>
      </tr>
    </thead>
    <tbody id="pool-rows"></tbody>
  </table>
  <div class="empty" id="pool-empty" style="display:none">Upstream pool disabled — add <code>upstream_pool.keys</code> to config.json.</div>

  <div style="margin-top:32px;display:flex;align-items:center;justify-content:space-between;flex-wrap:wrap;gap:12px">
    <h2 style="font-size:16px;font-weight:600">Models</h2>
    <div style="display:flex;gap:16px;font-size:12px;color:var(--muted)">
      <span><span class="dot" style="color:var(--green)">●</span> free</span>
      <span><span class="dot" style="color:var(--accent)">●</span> Go</span>
      <span><span class="dot" style="color:var(--yellow)">●</span> alias</span>
    </div>
  </div>
  <table style="margin-top:12px">
    <thead>
      <tr>
        <th>Model</th>
        <th>Catalog</th>
        <th>Upstream</th>
      </tr>
    </thead>
    <tbody id="model-rows"></tbody>
  </table>
  <div class="empty" id="models-empty" style="display:none">Models not loaded yet — catalogs refresh every 15 min.</div>

  <div class="footer" id="footer"></div>

<script>
async function refresh() {
  try {
    // forward ?token=... (dashboard auth) from the page URL to the data endpoint
    const q = location.search || '';
    const r = await fetch('/dashboard/data' + q);
    if (!r.ok) throw new Error('HTTP ' + r.status);
    const d = await r.json();
    document.getElementById('version').textContent = 'v' + (d.version || '');
    const rl = document.getElementById('rl-status');
    if (d.rate_limit && d.rate_limit.enabled) {
      rl.textContent = 'ON';
      rl.className = 'badge on';
      document.getElementById('rl-limits').textContent =
        d.rate_limit.requests_per_minute + ' req/min · ' +
        (d.rate_limit.tokens_per_minute || '∞') + ' tok/min';
    } else {
      rl.textContent = 'OFF';
      rl.className = 'badge off';
      document.getElementById('rl-limits').textContent = 'not configured';
    }
    const us = document.getElementById('usage-status');
    us.textContent = d.usage_enabled ? 'ON' : 'OFF';
    us.className = 'badge ' + (d.usage_enabled ? 'on' : 'off');
    let req = 0, tok = 0, cost = 0;
    const keys = Object.keys(d.usage || {}).sort();
    for (const k of keys) {
      req += d.usage[k].requests;
      tok += d.usage[k].total_tokens;
      cost += d.usage[k].cost_usd;
    }
    document.getElementById('usage-total').textContent = req + ' requests · ' + tok.toLocaleString() + ' tokens';
    document.getElementById('total-cost').textContent = '$' + cost.toFixed(4);

    const rows = document.getElementById('rows');
    rows.innerHTML = '';
    const empty = document.getElementById('empty');
    if (!keys.length) { empty.style.display = 'block'; }
    else { empty.style.display = 'none'; }
    for (const k of keys) {
      const u = d.usage[k];
      const rw = (d.rate_windows || {})[k] || {};
      const tr = document.createElement('tr');
      const last = u.last_used ? new Date(u.last_used).toLocaleString() : '—';
      tr.innerHTML =
        '<td class="key" title="' + k + '">' + esc(k) + '</td>' +
        '<td class="num">' + u.requests + '</td>' +
        '<td class="num">' + u.total_tokens.toLocaleString() + '</td>' +
        '<td class="num">$' + u.cost_usd.toFixed(4) + '</td>' +
        '<td class="num">' + last + '</td>' +
        '<td>' + rateCell(rw) + '</td>';
      rows.appendChild(tr);
    }

    // keys table
    const krows = document.getElementById('key-rows');
    krows.innerHTML = '';
    const keysList = d.keys || [];
    const kEmpty = document.getElementById('keys-empty');
    if (!keysList.length) { kEmpty.style.display = 'block'; }
    else { kEmpty.style.display = 'none'; }
    for (const k of keysList) {
      const tr = document.createElement('tr');
      const status = k.revoked
        ? '<span class="badge" style="background:rgba(248,81,73,.12);color:var(--red)">revoked</span>'
        : '<span class="badge" style="background:rgba(63,185,80,.12);color:var(--green)">active</span>';
      const models = k.model_allow && k.model_allow.length ? k.model_allow.join(', ') : '<span style="color:var(--muted)">all</span>';
      const budget = k.budget_usd ? '$' + k.budget_usd.toFixed(2) : '<span style="color:var(--muted)">∞</span>';
      const spend = '<span class="' + (k.budget_usd && k.spend_usd >= k.budget_usd ? 'limited' : '') + '">$' + (k.spend_usd || 0).toFixed(4) + '</span>';
      tr.innerHTML =
        '<td>' + esc(k.name) + '</td>' +
        '<td class="key" title="' + esc(k.hash) + '">' + esc(k.hash) + '</td>' +
        '<td>' + status + '</td>' +
        '<td class="num">' + budget + '</td>' +
        '<td class="num">' + spend + '</td>' +
        '<td style="color:var(--muted)">' + models + '</td>';
      krows.appendChild(tr);
    }

    // upstream pool table
    const prows = document.getElementById('pool-rows');
    prows.innerHTML = '';
    const pool = d.pool_status || [];
    const pEmpty = document.getElementById('pool-empty');
    if (!d.pool_enabled || !pool.length) { pEmpty.style.display = 'block'; }
    else { pEmpty.style.display = 'none'; }
    for (const k of pool) {
      const tr = document.createElement('tr');
      const cd = k.cooldown_until ? new Date(k.cooldown_until).toLocaleString() : '—';
      const status = k.exhausted
        ? '<span class="badge" style="background:rgba(248,81,73,.12);color:var(--red)">cooldown</span>'
        : '<span class="badge" style="background:rgba(63,185,80,.12);color:var(--green)">ready</span>';
      tr.innerHTML =
        '<td>' + esc(k.alias) + '</td>' +
        '<td>' + status + '</td>' +
        '<td class="num">' + cd + '</td>' +
        '<td class="num">' + k.use_count + '</td>' +
        '<td class="num">' + k.error_count + '</td>' +
        '<td style="color:var(--muted)">' + esc(k.last_error || '—') + '</td>';
      prows.appendChild(tr);
    }

    // models table
    const mrows = document.getElementById('model-rows');
    mrows.innerHTML = '';
    const models = d.models || [];
    const mEmpty = document.getElementById('models-empty');
    if (!models.length) { mEmpty.style.display = 'block'; }
    else { mEmpty.style.display = 'none'; }
    for (const m of models) {
      const tr = document.createElement('tr');
      const badges = [];
      if (m.is_free) badges.push('<span class="badge" style="background:rgba(63,185,80,.12);color:var(--green)">free</span>');
      if (m.is_go) badges.push('<span class="badge" style="background:rgba(88,166,255,.12);color:var(--accent)">Go</span>');
      if (m.is_alias) badges.push('<span class="badge" style="background:rgba(210,153,34,.12);color:var(--yellow)">alias</span>');
      const upstream = m.is_alias ? (m.upstream || '—') : '—';
      tr.innerHTML =
        '<td>' + esc(m.id) + '</td>' +
        '<td>' + (badges.join(' ') || '<span style="color:var(--muted)">—</span>') + '</td>' +
        '<td style="color:var(--muted)">' + esc(upstream) + '</td>';
      mrows.appendChild(tr);
    }
    document.getElementById('footer').textContent = 'generated ' + d.generated_at;
  } catch (e) {
    document.getElementById('footer').textContent = 'dashboard error: ' + e;
  }
}
function rateCell(rw) {
  if (!rw || !rw.requests_per_minute) return '<span class="muted" style="color:var(--muted)">—</span>';
  const pct = rw.requests_per_minute ? Math.round(100 * rw.requests / rw.requests_per_minute) : 0;
  const color = rw.limited ? 'var(--red)' : (pct > 80 ? 'var(--yellow)' : 'var(--green)');
  const w = Math.min(100, pct);
  return '<div style="width:120px;height:6px;background:var(--border);border-radius:3px">' +
         '<div style="width:' + w + '%;height:100%;background:' + color + ';border-radius:3px"></div></div>' +
         '<span style="font-size:10px;color:var(--muted)">' + rw.requests + '/' + rw.requests_per_minute +
         (rw.limited ? ' · limited' : '') + '</span>';
}
function esc(s) { return s.replace(/[&<>"]/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c])); }
refresh();
setInterval(refresh, 5000);
</script>
</body>
</html>`
