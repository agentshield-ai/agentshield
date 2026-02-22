let sqlJsReady = null;

const state = {
  all: [], filtered: [], selected: null,
  sort: { key: 'timestamp', dir: 'desc' },
  filters: { severity:new Set(), action:new Set(), tool:new Set(), rule:new Set(), timeRange:'all', search:'' }
};

const el = id => document.getElementById(id);
const sevOrder = { critical:4, high:3, medium:2, low:1 };

async function ensureSqlJs(){
  if (sqlJsReady) return sqlJsReady;
  sqlJsReady = new Promise((resolve, reject) => {
    if (window.initSqlJs) return resolve(window.initSqlJs);

    const trySrc = (src, onFail) => {
      const s = document.createElement('script');
      s.src = src;
      s.onload = () => resolve(window.initSqlJs);
      s.onerror = onFail;
      document.head.appendChild(s);
    };

    // Prefer local bundled asset (air-gapped capable), fallback to CDN.
    trySrc('./vendor/sql-wasm.js', () => {
      trySrc('https://cdn.jsdelivr.net/npm/sql.js@1.10.3/dist/sql-wasm.js', () => {
        reject(new Error('Failed to load sql.js (local and CDN both unavailable)'));
      });
    });
  });
  return sqlJsReady;
}

async function loadSqliteAlerts(file){
  const initSqlJs = await ensureSqlJs();
  const SQL = await initSqlJs({
    locateFile: (f) => {
      // file:// pages cannot reliably HEAD-check; default to local bundled path.
      return `./vendor/${f}`;
    },
  });
  const buf = await file.arrayBuffer();
  const db = new SQL.Database(new Uint8Array(buf));

  const candidates = [
    'SELECT id, rule_name, severity, tool, args, action_taken, timestamp, session_id, event_id FROM alerts ORDER BY timestamp DESC',
    'SELECT * FROM alerts ORDER BY timestamp DESC',
  ];

  let rows = null;
  for (const q of candidates){
    try {
      const res = db.exec(q);
      if (res && res[0]) {
        const cols = res[0].columns;
        rows = res[0].values.map(v => Object.fromEntries(cols.map((c,i)=>[c, v[i]])));
        break;
      }
    } catch {}
  }

  if (!rows) throw new Error('No readable alerts table found in SQLite DB');
  rows = rows.map(r => {
    if (typeof r.args === 'string') {
      try { r.args = JSON.parse(r.args); } catch {}
    }
    return r;
  });
  loadRows(rows, file.name + ' (sqlite)');
}

function normalise(row){
  return {
    id: row.id ?? null,
    timestamp: row.timestamp || row.created_at || new Date().toISOString(),
    rule_name: row.rule_name || row.rule || 'unknown-rule',
    severity: (row.severity || 'unknown').toLowerCase(),
    tool: row.tool || 'n/a',
    action_taken: row.action_taken || row.action || 'log',
    session_id: row.session_id || row.session || 'n/a',
    event_id: row.event_id || 'n/a',
    args: row.args,
    raw: row,
  };
}

function parseSearch(q){
  const parts = q.trim().split(/\s+/).filter(Boolean);
  const kv = {}; const words = [];
  for (const p of parts){
    const i = p.indexOf(':');
    if (i > 0) kv[p.slice(0,i).toLowerCase()] = p.slice(i+1).toLowerCase();
    else words.push(p.toLowerCase());
  }
  return { kv, words };
}

function applyFilters(){
  const now = Date.now();
  const {kv, words} = parseSearch(state.filters.search);
  state.filtered = state.all.filter(r => {
    if (state.filters.severity.size && !state.filters.severity.has(r.severity)) return false;
    if (state.filters.action.size && !state.filters.action.has(r.action_taken)) return false;
    if (state.filters.tool.size && !state.filters.tool.has(r.tool)) return false;
    if (state.filters.rule.size && !state.filters.rule.has(r.rule_name)) return false;

    if (state.filters.timeRange !== 'all'){
      const ms = state.filters.timeRange==='24h'?86400000:state.filters.timeRange==='7d'?604800000:2592000000;
      if (now - new Date(r.timestamp).getTime() > ms) return false;
    }

    for (const [k,v] of Object.entries(kv)){
      const val = String(r[k] ?? '').toLowerCase();
      if (!val.includes(v)) return false;
    }
    if (words.length){
      const blob = JSON.stringify(r.raw).toLowerCase();
      if (!words.every(w => blob.includes(w))) return false;
    }
    return true;
  });
  sortRows();
  render();
}

function sortRows(){
  const {key, dir} = state.sort;
  state.filtered.sort((a,b)=>{
    let av = a[key], bv = b[key];
    if (key==='severity'){ av = sevOrder[av] || 0; bv = sevOrder[bv] || 0; }
    if (key==='timestamp'){ av = new Date(av).getTime(); bv = new Date(bv).getTime(); }
    if (av < bv) return dir==='asc' ? -1 : 1;
    if (av > bv) return dir==='asc' ? 1 : -1;
    return 0;
  });
}

function countBy(field, rows=state.all){
  const m = new Map();
  for (const r of rows) m.set(r[field], (m.get(r[field])||0)+1);
  return [...m.entries()].sort((a,b)=>b[1]-a[1]);
}

function renderFacets(nodeId, field, targetSet, top=10){
  const node = el(nodeId); node.innerHTML = '';
  for (const [v,c] of countBy(field).slice(0, top)){
    const d = document.createElement('div'); d.className = 'facet' + (targetSet.has(v) ? ' active':'');
    d.textContent = `${v} (${c})`;
    d.onclick = ()=>{ targetSet.has(v)?targetSet.delete(v):targetSet.add(v); applyFilters(); };
    node.appendChild(d);
  }
}

function renderStats(){
  el('stat-total').textContent = state.filtered.length;
  el('stat-critical').textContent = state.filtered.filter(r=>r.severity==='critical').length;
  el('stat-blocked').textContent = state.filtered.filter(r=>String(r.action_taken).toLowerCase()==='block').length;
  el('stat-sessions').textContent = new Set(state.filtered.map(r=>r.session_id)).size;
}

function renderTable(){
  const tbody = el('table').querySelector('tbody'); tbody.innerHTML = '';
  for (const r of state.filtered.slice(0, 1000)){
    const tr = document.createElement('tr');
    tr.innerHTML = `<td>${new Date(r.timestamp).toISOString()}</td><td>${r.severity}</td><td>${r.rule_name}</td><td>${r.tool}</td><td>${r.action_taken}</td><td>${r.session_id}</td>`;
    tr.onclick = ()=>{
      state.selected = r;
      el('details').textContent = JSON.stringify(r.raw, null, 2);
      el('details-empty').style.display='none';
      el('notes').value = localStorage.getItem(`forensics:note:${r.event_id}`) || '';
    };
    tbody.appendChild(tr);
  }
}

function renderTrend(){
  const cv = el('trend'); const ctx = cv.getContext('2d');
  const bins = new Map();
  for (const r of state.filtered){
    const d = new Date(r.timestamp); d.setMinutes(0,0,0);
    const k = d.toISOString(); bins.set(k, (bins.get(k)||0)+1);
  }
  const points = [...bins.entries()].sort((a,b)=>a[0].localeCompare(b[0]));
  ctx.clearRect(0,0,cv.width,cv.height);
  if (!points.length) return;
  const max = Math.max(...points.map(p=>p[1]));
  const w = cv.width || cv.clientWidth; const h = cv.height;
  ctx.strokeStyle = '#4ea1ff'; ctx.lineWidth = 2; ctx.beginPath();
  points.forEach(([_,v],i)=>{
    const x = (i/(points.length-1||1))*(w-10)+5;
    const y = h - ((v/max)*(h-10)+5);
    if (i===0) ctx.moveTo(x,y); else ctx.lineTo(x,y);
  });
  ctx.stroke();
}

function render(){
  renderFacets('severity-facets','severity',state.filters.severity,5);
  renderFacets('action-facets','action_taken',state.filters.action,6);
  renderFacets('tool-facets','tool',state.filters.tool,10);
  renderFacets('rule-facets','rule_name',state.filters.rule,12);
  renderStats(); renderTrend(); renderTable();
}

function loadRows(rows, source='manual'){
  state.all = rows.map(normalise).filter(r=>!Number.isNaN(new Date(r.timestamp).getTime()));
  state.selected = null; el('details').textContent = ''; el('details-empty').style.display='block';
  el('source-meta').textContent = `Loaded ${state.all.length} alerts from ${source}`;
  applyFilters();
}

el('file').addEventListener('change', async (e)=>{
  const f = e.target.files[0]; if (!f) return;
  const txt = await f.text();
  let rows = [];
  try { rows = JSON.parse(txt); if (!Array.isArray(rows)) rows = rows.alerts || []; }
  catch { rows = txt.split(/\n+/).filter(Boolean).map(l=>JSON.parse(l)); }
  loadRows(rows, f.name);
});

el('db-file').addEventListener('change', async (e)=>{
  const f = e.target.files[0]; if (!f) return;
  try {
    await loadSqliteAlerts(f);
  } catch (err) {
    alert(`SQLite load failed: ${err.message}`);
  }
});

el('search').oninput = e => { state.filters.search = e.target.value; applyFilters(); };
el('time-range').onchange = e => { state.filters.timeRange = e.target.value; applyFilters(); };
el('clear-filters').onclick = ()=>{
  state.filters.severity.clear(); state.filters.action.clear(); state.filters.tool.clear(); state.filters.rule.clear();
  state.filters.timeRange = 'all'; state.filters.search = ''; el('search').value=''; el('time-range').value='all';
  applyFilters();
};

for (const th of document.querySelectorAll('th[data-sort]')){
  th.onclick = ()=>{
    const key = th.dataset.sort;
    state.sort.dir = (state.sort.key===key && state.sort.dir==='desc') ? 'asc' : 'desc';
    state.sort.key = key; applyFilters();
  };
}

el('save-view').onclick = ()=>{
  const name = prompt('View name?'); if (!name) return;
  const payload = {
    filters: {
      severity:[...state.filters.severity], action:[...state.filters.action], tool:[...state.filters.tool], rule:[...state.filters.rule],
      timeRange:state.filters.timeRange, search:state.filters.search
    },
    sort: state.sort
  };
  localStorage.setItem('forensics:view:'+name, JSON.stringify(payload));
  loadSavedViews();
};

function loadSavedViews(){
  const sel = el('saved-views'); sel.innerHTML = '<option value="">—</option>';
  Object.keys(localStorage).filter(k=>k.startsWith('forensics:view:')).sort().forEach(k=>{
    const o = document.createElement('option'); o.value=k; o.textContent=k.replace('forensics:view:',''); sel.appendChild(o);
  });
}

el('saved-views').onchange = e => {
  const k = e.target.value; if (!k) return;
  const v = JSON.parse(localStorage.getItem(k));
  state.filters.severity = new Set(v.filters.severity); state.filters.action = new Set(v.filters.action);
  state.filters.tool = new Set(v.filters.tool); state.filters.rule = new Set(v.filters.rule);
  state.filters.timeRange = v.filters.timeRange; state.filters.search = v.filters.search;
  state.sort = v.sort; el('search').value = state.filters.search; el('time-range').value = state.filters.timeRange;
  applyFilters();
};

el('export-json').onclick = ()=>{
  const blob = new Blob([JSON.stringify(state.filtered.map(r=>r.raw), null, 2)], {type:'application/json'});
  const a = document.createElement('a'); a.href = URL.createObjectURL(blob); a.download = 'alerts-filtered.json'; a.click();
  URL.revokeObjectURL(a.href);
};

el('save-note').onclick = ()=>{
  if (!state.selected) return alert('Select an alert first');
  const key = `forensics:note:${state.selected.event_id}`;
  localStorage.setItem(key, el('notes').value || '');
  alert('Note saved');
};

el('load-sample').onclick = ()=> loadRows([
  { id:1, timestamp:new Date(Date.now()-3600e3).toISOString(), severity:'critical', rule_name:'Remote Code Execution via Piped Script Download', tool:'exec', action_taken:'block', session_id:'session-a', event_id:'evt-1', args:{command:'curl x|bash'} },
  { id:2, timestamp:new Date(Date.now()-1800e3).toISOString(), severity:'high', rule_name:'OpenClaw Session Spawn Abuse', tool:'sessions_spawn', action_taken:'log', session_id:'session-b', event_id:'evt-2' },
  { id:3, timestamp:new Date(Date.now()-900e3).toISOString(), severity:'medium', rule_name:'Suspicious File Write', tool:'write', action_taken:'log', session_id:'session-a', event_id:'evt-3' }
], 'demo-data');

loadSavedViews();
loadRows([], 'empty');