// AgentShield SIEM Investigation Console
// ─── State ───
const S = {
  token: localStorage.getItem('as_token') || '',
  base: localStorage.getItem('as_base') || '',
  view: 'overview',
  hours: 24,
  page: { offset: 0, limit: 50 },
  alertFilter: { severity: '', rule: '', session_id: '' },
};

// ─── API ───
async function api(path, params = {}) {
  const u = new URL(path, S.base || location.origin);
  Object.entries(params).forEach(([k, v]) => {
    if (v !== '' && v != null) u.searchParams.set(k, v);
  });
  const h = {};
  if (S.token) h['Authorization'] = 'Bearer ' + S.token;
  const r = await fetch(u, { headers: h });
  if (!r.ok) throw new Error(`${r.status} ${r.statusText}`);
  return r.json();
}

// ─── Helpers ───
const $ = (s, c) => (c || document).querySelector(s);
const $$ = (s, c) => [...(c || document).querySelectorAll(s)];
const h = s => String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
function relTime(ts) {
  const d = (Date.now() - new Date(ts).getTime()) / 1000;
  if (d < 60) return `${Math.round(d)}s ago`;
  if (d < 3600) return `${Math.round(d/60)}m ago`;
  if (d < 86400) return `${Math.round(d/3600)}h ago`;
  return `${Math.round(d/86400)}d ago`;
}
function fmtTime(ts) {
  const d = new Date(ts);
  return d.toLocaleString(undefined, { month:'short', day:'numeric', hour:'2-digit', minute:'2-digit', second:'2-digit', hour12:false });
}
function sevPill(s) {
  return `<span class="sev sev-${h(s||'')}">${h(s||'none')}</span>`;
}
function actionPill(a) {
  return `<span class="action action-${h(a||'')}">${h(a||'')}</span>`;
}
function prettyJSON(str) {
  try {
    const obj = typeof str === 'string' ? JSON.parse(str) : str;
    return JSON.stringify(obj, null, 2)
      .replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;')
      .replace(/"([^"]+)"(?=\s*:)/g, '"<span class="k">$1</span>"')
      .replace(/: "([^"]*)"/g, ': "<span class="s">$1</span>"')
      .replace(/: (\d+\.?\d*)/g, ': <span class="n">$1</span>');
  } catch { return h(str); }
}
function num(n) { return Number(n||0).toLocaleString(); }

// ─── Toast ───
let _toastTimer;
function toast(msg, type = 'ok') {
  const el = $('#toast');
  el.textContent = msg;
  el.className = `toast ${type} show`;
  clearTimeout(_toastTimer);
  _toastTimer = setTimeout(() => el.classList.remove('show'), 3500);
}

// ─── Chart renderers ───
function renderTimeline(buckets, width = 800, height = 160) {
  if (!buckets || !buckets.length) return '<div class="empty">No data in this time range</div>';
  const pad = { t: 10, r: 10, b: 28, l: 44 };
  const w = width - pad.l - pad.r;
  const bw = Math.max(3, Math.min(28, (w / buckets.length) - 2));
  const maxVal = Math.max(1, ...buckets.map(b => b.total));
  const scaleY = v => (height - pad.t - pad.b) * (1 - v / maxVal);
  let bars = '';
  buckets.forEach((b, i) => {
    const x = pad.l + i * (w / buckets.length) + (w / buckets.length - bw) / 2;
    let y = 0;
    const segs = [
      { key: 'low', val: b.low },
      { key: 'medium', val: b.medium },
      { key: 'high', val: b.high },
      { key: 'critical', val: b.critical },
    ];
    segs.forEach(s => {
      if (!s.val) return;
      const sh = (s.val / maxVal) * (height - pad.t - pad.b);
      const sy = height - pad.b - y - sh;
      bars += `<rect class="bar bar-${s.key}" x="${x}" y="${sy}" width="${bw}" rx="2" height="${sh}"><title>${fmtTime(b.bucket)}: ${s.val} ${s.key}</title></rect>`;
      y += sh;
    });
    // x-axis label every few bars
    if (i % Math.max(1, Math.round(buckets.length / 8)) === 0) {
      const d = new Date(b.bucket);
      const label = d.toLocaleTimeString(undefined, { hour:'2-digit', minute:'2-digit', hour12:false });
      bars += `<text class="axis" x="${x + bw/2}" y="${height - 6}" text-anchor="middle">${label}</text>`;
    }
  });
  // y-axis gridlines
  let grid = '';
  for (let i = 0; i <= 3; i++) {
    const yy = pad.t + i * ((height - pad.t - pad.b) / 3);
    const val = Math.round(maxVal * (1 - i / 3));
    grid += `<line class="grid-line" x1="${pad.l}" y1="${yy}" x2="${width - pad.r}" y2="${yy}"/>`;
    grid += `<text class="axis" x="${pad.l - 6}" y="${yy + 3}" text-anchor="end">${val}</text>`;
  }
  return `<svg class="chart" viewBox="0 0 ${width} ${height}" preserveAspectRatio="xMidYMid meet">${grid}${bars}</svg>
    <div class="chart-legend">
      <span><i style="background:var(--critical)"></i>Critical</span>
      <span><i style="background:var(--high)"></i>High</span>
      <span><i style="background:var(--medium)"></i>Medium</span>
      <span><i style="background:var(--low)"></i>Low</span>
    </div>`;
}

function renderDonut(data, size = 140) {
  // data = { critical: N, high: N, medium: N, low: N }
  const total = Object.values(data).reduce((a, b) => a + b, 0);
  if (!total) return '<div class="empty">No alerts</div>';
  const colors = { critical: 'var(--critical)', high: 'var(--high)', medium: 'var(--medium)', low: 'var(--low)' };
  const r = size / 2 - 6, cx = size / 2, cy = size / 2;
  const circ = 2 * Math.PI * r;
  let offset = 0;
  let arcs = '';
  const order = ['critical', 'high', 'medium', 'low'];
  order.forEach(k => {
    const v = data[k] || 0;
    if (!v) return;
    const pct = v / total;
    const dashLen = circ * pct;
    arcs += `<circle r="${r}" cx="${cx}" cy="${cy}" fill="none" stroke="${colors[k]}" stroke-width="10" stroke-dasharray="${dashLen} ${circ - dashLen}" stroke-dashoffset="${-offset}" transform="rotate(-90 ${cx} ${cy})" opacity="0.9"/>`;
    offset += dashLen;
  });
  const svg = `<svg width="${size}" height="${size}" viewBox="0 0 ${size} ${size}">${arcs}<text x="${cx}" y="${cy}" text-anchor="middle" dy="0.35em" fill="var(--text)" font-size="22" font-weight="700" font-family="var(--sans)">${num(total)}</text></svg>`;
  let legend = '<div class="donut-legend">';
  order.forEach(k => {
    if (!data[k]) return;
    legend += `<div class="row"><span class="dot" style="background:${colors[k]}"></span><span class="label">${k}</span><span class="count">${num(data[k])}</span></div>`;
  });
  legend += '</div>';
  return `<div class="donut-wrap">${svg}${legend}</div>`;
}

// ─── Drawer ───
let _drawerOpen = false;
function openDrawer(title, html) {
  let drawer = $('#drawer');
  let scrim = $('#scrim');
  if (!drawer) {
    document.body.insertAdjacentHTML('beforeend', '<div id="scrim" class="scrim"></div><div id="drawer" class="drawer"><div class="drawer-head"><h2></h2><button class="icon-btn" id="drawer-close">✕</button></div><div class="drawer-body"></div></div>');
    drawer = $('#drawer'); scrim = $('#scrim');
    scrim.addEventListener('click', closeDrawer);
    $('#drawer-close').addEventListener('click', closeDrawer);
  }
  $('h2', drawer).textContent = title;
  $('.drawer-body', drawer).innerHTML = html;
  requestAnimationFrame(() => { drawer.classList.add('open'); scrim.classList.add('open'); });
  _drawerOpen = true;
}
function closeDrawer() {
  const d = $('#drawer'), s = $('#scrim');
  if (d) d.classList.remove('open');
  if (s) s.classList.remove('open');
  _drawerOpen = false;
}

// ─── Alert detail ───
async function showAlertDetail(id) {
  openDrawer('Alert #' + id, '<div class="skeleton" style="height:200px"></div>');
  try {
    const a = await api(`/api/v1/alerts/${id}`);
    let argsHTML = '';
    if (a.args) {
      argsHTML = `<pre class="json">${prettyJSON(a.args)}</pre>`;
    }
    openDrawer('Alert #' + id, `
      <dl class="kv">
        <dt>ID</dt><dd>${h(a.id)}</dd>
        <dt>Event ID</dt><dd>${h(a.event_id||'—')}</dd>
        <dt>Session</dt><dd>${a.session_id ? `<a href="#" onclick="event.preventDefault();closeDrawer();navigateTo('sessions','${h(a.session_id)}')">${h(a.session_id)}</a>` : '—'}</dd>
        <dt>Rule</dt><dd>${h(a.rule_name)}</dd>
        <dt>Severity</dt><dd>${sevPill(a.severity)}</dd>
        <dt>Action</dt><dd>${actionPill(a.action_taken)}</dd>
        <dt>Tool</dt><dd class="mono">${h(a.tool||'—')}</dd>
        <dt>Timestamp</dt><dd>${fmtTime(a.timestamp)}</dd>
      </dl>
      ${argsHTML ? '<h3 style="margin:10px 0 6px;font-size:12px;color:var(--text-dim)">ARGUMENTS</h3>' + argsHTML : ''}
    `);
  } catch (e) { openDrawer('Error', `<p class="muted">${h(e.message)}</p>`); }
}

// ─── Views ───
const views = {};

// --- Overview ---
views.overview = async function() {
  const v = $('#view');
  v.innerHTML = `
    <div class="card-row grid-4" id="kpi-row">${'<div class="card kpi"><div class="skeleton" style="height:48px"></div></div>'.repeat(4)}</div>
    <div class="card-row grid-3">
      <div class="card" id="timeline-card"><h3>Alert Timeline</h3><div class="skeleton" style="height:170px"></div></div>
      <div class="card" id="donut-card"><h3>Severity Distribution</h3><div class="skeleton" style="height:160px"></div></div>
      <div class="card" id="tools-card"><h3>Top Tools</h3><div class="skeleton" style="height:160px"></div></div>
    </div>
    <div class="card-row grid-2">
      <div class="card" id="rules-card"><h3>Top Rules</h3><div class="skeleton" style="height:120px"></div></div>
      <div class="card" id="recent-card"><h3>Recent Critical/High Alerts</h3><div class="skeleton" style="height:120px"></div></div>
    </div>`;

  const hrs = S.hours;
  const bucketMin = hrs <= 6 ? 15 : hrs <= 24 ? 60 : hrs <= 168 ? 360 : 1440;

  try {
    const [stats, timeline, rules, tools, recent] = await Promise.all([
      api('/api/v1/stats'),
      api('/api/v1/timeline', { hours: hrs, bucket_minutes: bucketMin }),
      api('/api/v1/rules', { limit: 8 }),
      api('/api/v1/top-tools', { limit: 8 }),
      api('/api/v1/alerts', { limit: 8, severity: 'critical' }),
    ]);

    // Also get high alerts if critical is sparse
    let recentAlerts = (recent.alerts || []);
    if (recentAlerts.length < 8) {
      const hi = await api('/api/v1/alerts', { limit: 8 - recentAlerts.length, severity: 'high' });
      recentAlerts = recentAlerts.concat(hi.alerts || []);
    }

    const sev = stats.by_severity || {};
    const total = stats.total_alerts || 0;

    // KPI
    $('#kpi-row').innerHTML = `
      <div class="card kpi"><h3>Total Alerts</h3><div class="value">${num(total)}</div><div class="delta">${num(stats.recent_24h||0)} last 24h</div></div>
      <div class="card kpi"><h3>Critical</h3><div class="value" style="color:var(--critical)">${num(sev.critical||0)}</div></div>
      <div class="card kpi"><h3>High</h3><div class="value" style="color:var(--high)">${num(sev.high||0)}</div></div>
      <div class="card kpi"><h3>Blocked</h3><div class="value" style="color:var(--danger)">${num((stats.by_action||{}).block||0)}</div><div class="delta">${num((stats.by_action||{}).require_approval||0)} pending approval</div></div>`;

    // Timeline
    const tlc = $('#timeline-card');
    tlc.innerHTML = '<h3>Alert Timeline</h3>' + renderTimeline(timeline, tlc.clientWidth - 32, 180);

    // Donut
    $('#donut-card').innerHTML = '<h3>Severity Distribution</h3>' + renderDonut(sev);

    // Top tools
    const tc = $('#tools-card');
    if (tools && tools.length) {
      const maxC = Math.max(1, ...tools.map(t => t.count));
      tc.innerHTML = '<h3>Top Tools</h3>' + tools.map(t =>
        `<div style="display:flex;align-items:center;gap:10px;margin-bottom:6px">
           <span class="mono" style="min-width:120px;text-align:right;font-size:12px;color:var(--text-dim)">${h(t.tool)}</span>
           <div style="flex:1;height:14px;background:var(--surface-2);border-radius:7px;overflow:hidden">
             <div style="width:${(t.count/maxC)*100}%;height:100%;background:linear-gradient(90deg,var(--accent),var(--accent-2));border-radius:7px;transition:width .3s"></div>
           </div>
           <span style="font-size:12px;color:var(--text);font-variant-numeric:tabular-nums;min-width:32px">${num(t.count)}</span>
         </div>`).join('');
    } else { tc.innerHTML = '<h3>Top Tools</h3><div class="empty">No data</div>'; }

    // Rules
    const rc = $('#rules-card');
    if (rules && rules.length) {
      rc.innerHTML = '<h3>Top Rules</h3><div class="table-wrap"><table class="data"><thead><tr><th>Rule</th><th>Severity</th><th>Count</th><th>Last Seen</th></tr></thead><tbody>' +
        rules.map(r => `<tr onclick="navigateTo('alerts','','${h(r.rule_name)}')"><td class="truncate">${h(r.rule_name)}</td><td>${sevPill(r.severity)}</td><td class="num">${num(r.alert_count)}</td><td class="mono">${relTime(r.last_seen)}</td></tr>`).join('') +
        '</tbody></table></div>';
    } else { rc.innerHTML = '<h3>Top Rules</h3><div class="empty">No rules</div>'; }

    // Recent critical/high
    const rec = $('#recent-card');
    if (recentAlerts.length) {
      rec.innerHTML = '<h3>Recent Critical/High Alerts</h3><div class="table-wrap"><table class="data"><thead><tr><th>Time</th><th>Rule</th><th>Severity</th><th>Tool</th><th>Action</th></tr></thead><tbody>' +
        recentAlerts.map(a => `<tr onclick="showAlertDetail(${a.id})"><td class="mono">${relTime(a.timestamp)}</td><td class="truncate">${h(a.rule_name)}</td><td>${sevPill(a.severity)}</td><td class="mono">${h(a.tool||'—')}</td><td>${actionPill(a.action_taken)}</td></tr>`).join('') +
        '</tbody></table></div>';
    } else { rec.innerHTML = '<h3>Recent Critical/High Alerts</h3><div class="empty">No critical/high alerts</div>'; }

  } catch (e) {
    toast(e.message, 'err');
    v.innerHTML = `<div class="empty">Failed to load overview: ${h(e.message)}</div>`;
  }
};

// --- Alerts ---
views.alerts = async function(_, filterRule) {
  const v = $('#view');
  v.innerHTML = `
    <div class="filter-bar">
      <input id="alert-search" type="text" placeholder="Search by event_id, session_id, or rule...">
      <select id="alert-sev-filter">
        <option value="">All severities</option>
        <option value="critical">Critical</option>
        <option value="high">High</option>
        <option value="medium">Medium</option>
        <option value="low">Low</option>
      </select>
      <button class="icon-btn" id="alert-apply">↻</button>
    </div>
    <div id="alerts-table"><div class="skeleton" style="height:300px"></div></div>
    <div class="pager" id="alerts-pager"></div>`;

  if (filterRule) {
    $('#alert-search').value = filterRule;
    S.alertFilter.rule = filterRule;
  }

  async function load() {
    const search = $('#alert-search').value.trim();
    const sev = $('#alert-sev-filter').value;
    const params = { limit: S.page.limit, offset: S.page.offset };
    if (sev) params.severity = sev;
    // Try to detect if search is rule, session_id, or event_id
    if (search) {
      if (search.startsWith('sess') || search.length === 36 || search.includes('-')) {
        params.session_id = search;
      } else if (search.startsWith('evt') || search.startsWith('event')) {
        // nothing — we can try both
      } else {
        params.rule = search;
      }
    }
    const since = new Date(Date.now() - S.hours * 3600000).toISOString();
    params.since = since;

    try {
      const data = await api('/api/v1/alerts', params);
      const alerts = data.alerts || [];
      const total = data.total_count || 0;

      if (!alerts.length) {
        $('#alerts-table').innerHTML = '<div class="empty">No alerts found</div>';
        $('#alerts-pager').innerHTML = '';
        return;
      }

      $('#alerts-table').innerHTML = `<div class="table-wrap"><table class="data"><thead><tr>
        <th>ID</th><th>Time</th><th>Rule</th><th>Severity</th><th>Tool</th><th>Action</th><th>Session</th>
      </tr></thead><tbody>` +
        alerts.map(a => `<tr onclick="showAlertDetail(${a.id})">
          <td class="mono">${a.id}</td>
          <td class="mono">${fmtTime(a.timestamp)}</td>
          <td class="truncate">${h(a.rule_name)}</td>
          <td>${sevPill(a.severity)}</td>
          <td class="mono">${h(a.tool||'—')}</td>
          <td>${actionPill(a.action_taken)}</td>
          <td class="mono truncate">${a.session_id ? `<a href="#" onclick="event.stopPropagation();event.preventDefault();navigateTo('sessions','${h(a.session_id)}')">${h(a.session_id.slice(0,12))}…</a>` : '—'}</td>
        </tr>`).join('') + '</tbody></table></div>';

      const showing = Math.min(S.page.offset + S.page.limit, total);
      $('#alerts-pager').innerHTML = `
        <span>${num(S.page.offset + 1)}–${num(showing)} of ${num(total)}</span>
        <div style="display:flex;gap:6px">
          <button ${S.page.offset === 0 ? 'disabled' : ''} id="pager-prev">← Prev</button>
          <button ${showing >= total ? 'disabled' : ''} id="pager-next">Next →</button>
        </div>`;
      const prev = $('#pager-prev');
      const next = $('#pager-next');
      if (prev) prev.onclick = () => { S.page.offset = Math.max(0, S.page.offset - S.page.limit); load(); };
      if (next) next.onclick = () => { S.page.offset += S.page.limit; load(); };
    } catch (e) {
      $('#alerts-table').innerHTML = `<div class="empty">Error: ${h(e.message)}</div>`;
    }
  }

  $('#alert-apply').onclick = () => { S.page.offset = 0; load(); };
  $('#alert-search').addEventListener('keydown', e => { if (e.key === 'Enter') { S.page.offset = 0; load(); } });
  $('#alert-sev-filter').addEventListener('change', () => { S.page.offset = 0; load(); });

  S.page.offset = 0;
  load();
};

// --- Sessions ---
views.sessions = async function(sessionId) {
  const v = $('#view');

  if (sessionId) {
    // Session drill-down
    v.innerHTML = `<div style="display:flex;gap:10px;align-items:center;margin-bottom:4px">
      <button class="icon-btn" onclick="navigateTo('sessions')">←</button>
      <h2 style="margin:0;font-size:15px">Session: <span class="mono">${h(sessionId)}</span></h2>
    </div><div id="sess-detail"><div class="skeleton" style="height:300px"></div></div>`;
    $('#crumb-detail').textContent = '/ ' + sessionId.slice(0, 16) + '…';

    try {
      const alerts = await api(`/api/v1/sessions/${sessionId}/alerts`);
      const el = $('#sess-detail');
      if (!alerts || !alerts.length) { el.innerHTML = '<div class="empty">No alerts for this session</div>'; return; }

      el.innerHTML = '<div class="session-timeline">' + alerts.map(a =>
        `<div class="event" onclick="showAlertDetail(${a.id})">
          <div class="t">${fmtTime(a.timestamp)}</div>
          <div class="body">
            <div class="rule">${h(a.rule_name)}</div>
            <div class="meta">${sevPill(a.severity)} ${actionPill(a.action_taken)} <span class="mono">${h(a.tool||'')}</span></div>
          </div>
        </div>`).join('') + '</div>';
    } catch (e) { $('#sess-detail').innerHTML = `<div class="empty">Error: ${h(e.message)}</div>`; }
    return;
  }

  // Sessions list
  v.innerHTML = '<div id="sess-list"><div class="skeleton" style="height:300px"></div></div>';
  $('#crumb-detail').textContent = '';

  try {
    const since = new Date(Date.now() - S.hours * 3600000).toISOString();
    const sessions = await api('/api/v1/sessions', { limit: 100, since });
    const el = $('#sess-list');
    if (!sessions || !sessions.length) { el.innerHTML = '<div class="empty">No sessions found</div>'; return; }

    el.innerHTML = `<div class="table-wrap"><table class="data"><thead><tr>
      <th>Session ID</th><th>Alerts</th><th>Top Severity</th><th>Rules</th><th>Tools</th><th>First Seen</th><th>Last Seen</th>
    </tr></thead><tbody>` +
      sessions.map(s => `<tr onclick="navigateTo('sessions','${h(s.session_id)}')">
        <td class="mono">${h(s.session_id.slice(0,20))}…</td>
        <td class="num">${num(s.alert_count)}</td>
        <td>${sevPill(s.top_severity)}</td>
        <td class="num">${num(s.distinct_rules)}</td>
        <td class="num">${num(s.distinct_tools)}</td>
        <td class="mono">${fmtTime(s.first_seen)}</td>
        <td class="mono">${relTime(s.last_seen)}</td>
      </tr>`).join('') + '</tbody></table></div>';
  } catch (e) { $('#sess-list').innerHTML = `<div class="empty">Error: ${h(e.message)}</div>`; }
};

// --- Rules ---
views.rules = async function() {
  const v = $('#view');
  v.innerHTML = '<div id="rules-list"><div class="skeleton" style="height:300px"></div></div>';
  try {
    const rules = await api('/api/v1/rules', { limit: 200 });
    const el = $('#rules-list');
    if (!rules || !rules.length) { el.innerHTML = '<div class="empty">No rules with alerts</div>'; return; }

    el.innerHTML = `<div class="table-wrap"><table class="data"><thead><tr>
      <th>Rule</th><th>Severity</th><th>Alerts</th><th>FP</th><th>TP</th><th>Last Seen</th>
    </tr></thead><tbody>` +
      rules.map(r => `<tr onclick="navigateTo('alerts','','${h(r.rule_name)}')">
        <td class="truncate">${h(r.rule_name)}</td>
        <td>${sevPill(r.severity)}</td>
        <td class="num">${num(r.alert_count)}</td>
        <td class="num" style="color:var(--warn)">${num(r.fp_count)}</td>
        <td class="num" style="color:var(--success)">${num(r.tp_count)}</td>
        <td class="mono">${relTime(r.last_seen)}</td>
      </tr>`).join('') + '</tbody></table></div>';
  } catch (e) { $('#rules-list').innerHTML = `<div class="empty">Error: ${h(e.message)}</div>`; }
};

// --- Investigate ---
views.investigate = async function() {
  const v = $('#view');
  v.innerHTML = `
    <div class="card">
      <h3>Forensic Search</h3>
      <p style="margin:0 0 10px;color:var(--text-dim);font-size:13px">Enter an Event ID, Alert ID, or Session ID to investigate.</p>
      <div class="investigate-form">
        <input id="inv-input" type="text" placeholder="e.g. evt-abc123, 42, or session-xyz…" autofocus>
        <button id="inv-btn">Investigate</button>
      </div>
    </div>
    <div id="inv-result"></div>`;

  async function investigate() {
    const q = $('#inv-input').value.trim();
    if (!q) return;
    const el = $('#inv-result');
    el.innerHTML = '<div class="skeleton" style="height:200px"></div>';

    try {
      // If numeric, assume alert ID
      if (/^\d+$/.test(q)) {
        const a = await api(`/api/v1/alerts/${q}`);
        if (!a || !a.id) { el.innerHTML = '<div class="empty">Alert not found</div>'; return; }
        el.innerHTML = `<div class="card"><h3>Alert #${a.id}</h3>
          <dl class="kv">
            <dt>Event ID</dt><dd class="mono">${h(a.event_id||'—')}</dd>
            <dt>Session</dt><dd>${a.session_id ? `<a href="#" onclick="event.preventDefault();navigateTo('sessions','${h(a.session_id)}')">${h(a.session_id)}</a>` : '—'}</dd>
            <dt>Rule</dt><dd>${h(a.rule_name)}</dd>
            <dt>Severity</dt><dd>${sevPill(a.severity)}</dd>
            <dt>Action</dt><dd>${actionPill(a.action_taken)}</dd>
            <dt>Tool</dt><dd class="mono">${h(a.tool||'—')}</dd>
            <dt>Timestamp</dt><dd>${fmtTime(a.timestamp)}</dd>
          </dl>
          ${a.args ? '<h3 style="margin:14px 0 6px">Arguments</h3><pre class="json">' + prettyJSON(a.args) + '</pre>' : ''}
        </div>`;
        return;
      }

      // Otherwise, search for session, then try as event_id
      const [sessAlerts, byEvent] = await Promise.allSettled([
        api('/api/v1/alerts', { session_id: q, limit: 100 }),
        api('/api/v1/alerts', { limit: 20 }),
      ]);

      let found = false;
      let html = '';

      if (sessAlerts.status === 'fulfilled' && sessAlerts.value.alerts && sessAlerts.value.alerts.length) {
        const alerts = sessAlerts.value.alerts;
        found = true;
        html += `<div class="card"><h3>Session Alerts (${alerts.length})</h3>
          <div class="session-timeline">` +
          alerts.map(a => `<div class="event" onclick="showAlertDetail(${a.id})">
            <div class="t">${fmtTime(a.timestamp)}</div>
            <div class="body"><div class="rule">${h(a.rule_name)}</div>
            <div class="meta">${sevPill(a.severity)} ${actionPill(a.action_taken)} <span class="mono">${h(a.tool||'')}</span></div></div>
          </div>`).join('') + '</div></div>';
      }

      if (!found) {
        html = '<div class="empty">No results found. Try an alert ID (number), session ID, or event ID.</div>';
      }
      el.innerHTML = html;
    } catch (e) { el.innerHTML = `<div class="empty">Error: ${h(e.message)}</div>`; }
  }

  $('#inv-btn').onclick = investigate;
  $('#inv-input').addEventListener('keydown', e => { if (e.key === 'Enter') investigate(); });
};

// ─── Navigation ───
window.navigateTo = function(view, detail, extra) {
  closeDrawer();
  S.view = view;
  $$('.sidebar nav a').forEach(a => a.classList.toggle('active', a.dataset.view === view));
  $('#crumb-view').textContent = view.charAt(0).toUpperCase() + view.slice(1);
  $('#crumb-detail').textContent = '';
  if (views[view]) views[view](detail, extra);
};

function route() {
  const hash = location.hash.slice(2) || 'overview';
  const [view, ...rest] = hash.split('/');
  navigateTo(view, rest.join('/'));
}

// ─── Health check ───
async function checkHealth() {
  try {
    await api('/api/v1/health');
    $('#conn-status').className = 'status-dot ok';
    $('#conn-status').title = 'Engine connected';
  } catch {
    $('#conn-status').className = 'status-dot err';
    $('#conn-status').title = 'Engine unreachable';
  }
}

// ─── Auth ───
function requireAuth() {
  if (S.token) {
    $('#login').classList.add('hidden');
    return true;
  }
  $('#login').classList.remove('hidden');
  return false;
}

$('#login-form').addEventListener('submit', e => {
  e.preventDefault();
  const base = $('#login-base').value.trim();
  const token = $('#login-token').value.trim();
  if (!token) { toast('Token required', 'err'); return; }
  S.token = token;
  S.base = base || '';
  localStorage.setItem('as_token', token);
  localStorage.setItem('as_base', base);
  $('#login').classList.add('hidden');
  boot();
});

$('#logout-btn').addEventListener('click', () => {
  localStorage.removeItem('as_token');
  localStorage.removeItem('as_base');
  S.token = '';
  S.base = '';
  location.reload();
});

$('#theme-toggle').addEventListener('click', () => {
  const root = document.documentElement;
  const curr = root.getAttribute('data-theme');
  root.setAttribute('data-theme', curr === 'light' ? '' : 'light');
  localStorage.setItem('as_theme', curr === 'light' ? '' : 'light');
});

$('#time-range').addEventListener('change', e => {
  S.hours = parseInt(e.target.value, 10);
  route();
});

$('#refresh-btn').addEventListener('click', () => route());

// Apply saved theme
if (localStorage.getItem('as_theme') === 'light') {
  document.documentElement.setAttribute('data-theme', 'light');
}

// ─── Boot ───
function boot() {
  checkHealth();
  setInterval(checkHealth, 30000);
  window.addEventListener('hashchange', route);
  route();
}

// Expose for inline onclick handlers
window.showAlertDetail = showAlertDetail;
window.closeDrawer = closeDrawer;

if (requireAuth()) boot();
