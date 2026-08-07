package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"time"
)

// dashboardData is the JSON payload served by /dashboard/data.
type dashboardData struct {
	GeneratedAt  string                 `json:"generated_at"`
	RateLimit    *RateLimitConfig       `json:"rate_limit"`
	RateWindows  map[string]RateStatus  `json:"rate_windows"`
	UsageEnabled bool                   `json:"usage_enabled"`
	Usage        map[string]KeyUsage    `json:"usage"`
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

  <div class="footer" id="footer"></div>

<script>
async function refresh() {
  try {
    const r = await fetch('/dashboard/data');
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
