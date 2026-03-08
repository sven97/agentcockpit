const $ = id => document.getElementById(id);
const currentPath = location.pathname.replace(/\/+$/, '') || '/';

const state = {
  token: localStorage.getItem('ac_token'),
  hosts: [],
  sessions: [],
  approvals: [],
  sessionTokens: {}, // sessionId → { inputTokens, contextWindowSize }
  pendingDevices: [],
  ws: null,
  wsState: 'disconnected',
};

const USE_CASES = {
  solo: {
    title: 'Late-night coding without risky blind execution',
    summary: 'You let Claude Code or Codex CLI continue working while you still approve sensitive operations before they run.',
    timeline: [
      '<b>Agent requests:</b> shell command, write, or other risky action.',
      '<b>You review:</b> exact input and risk level in dashboard.',
      '<b>You respond:</b> allow safe actions or deny unsafe ones.',
    ],
    outcome: 'You keep safety gates on while still getting autonomous coding speed.',
  },
  team: {
    title: 'Approve remote workstation sessions from anywhere',
    summary: 'When your session runs on your desktop, you can review and approve from your phone or another laptop.',
    timeline: [
      '<b>Session runs:</b> approvals stream to your dashboard in real time.',
      '<b>You validate:</b> what the agent is trying to run.',
      '<b>Work continues:</b> no manual reconnect loops or SSH juggling.',
    ],
    outcome: 'You stay in control even when you are not at your main machine.',
  },
  consulting: {
    title: 'Client delivery with clear, reviewable decisions',
    summary: 'When using OpenCode, Claude Code, or Codex CLI for client work, you can show exactly what was requested and approved.',
    timeline: [
      '<b>Work starts:</b> approvals appear with clear context.',
      '<b>Sensitive step:</b> inspect details before execution.',
      '<b>Scope control:</b> approve only what matches agreement.',
    ],
    outcome: 'You provide confidence and traceability without slowing delivery.',
  },
};

// ── API ────────────────────────────────────────────────────────────────────────

async function api(method, path, body) {
  try {
    const res = await fetch(path, {
      method,
      headers: {
        'Authorization': `Bearer ${state.token}`,
        'Content-Type': 'application/json',
      },
      body: body ? JSON.stringify(body) : undefined,
    });
    if (res.status === 401) { logout(); return null; }
    if (!res.ok) {
      const text = await res.text();
      throw new Error(text || res.statusText);
    }
    if (res.status === 204 || res.headers.get('content-length') === '0') return null;
    const ct = res.headers.get('content-type') || '';
    if (ct.includes('application/json')) return res.json();
    return null;
  } catch (e) {
    if (e.name !== 'TypeError') throw e; // network error
    return null;
  }
}

// ── Auth ───────────────────────────────────────────────────────────────────────

function showAuth() {
  if (currentPath !== '/login') {
    location.href = '/login';
    return;
  }
  $('home').classList.remove('visible');
  $('auth').classList.add('visible');
  $('app').classList.remove('visible');
}

function showApp() {
  if (currentPath !== '/dashboard') {
    location.href = '/dashboard';
    return;
  }
  $('home').classList.remove('visible');
  $('auth').classList.remove('visible');
  $('app').classList.add('visible');
  fetchAll();
  connectWS();
}

function showHome() {
  if (currentPath !== '/') {
    location.href = '/';
    return;
  }
  $('home').classList.add('visible');
  $('auth').classList.remove('visible');
  $('app').classList.remove('visible');
}

function setAuthMode(mode) {
  const login = mode !== 'register';
  $('login-form').style.display = login ? 'flex' : 'none';
  $('register-form').style.display = login ? 'none' : 'flex';
  $('auth-error').textContent = '';
  $('reg-error').textContent = '';
}

function renderUseCase(key) {
  const uc = USE_CASES[key] || USE_CASES.solo;
  $('usecase-title').textContent = uc.title;
  $('usecase-summary').textContent = uc.summary;
  $('usecase-timeline').innerHTML = uc.timeline.map(line => `
    <div class="timeline-row">
      <span class="timeline-dot"></span>
      <span>${line}</span>
    </div>
  `).join('');
  $('usecase-outcome').textContent = uc.outcome;
  document.querySelectorAll('.usecase-btn').forEach(btn => {
    btn.classList.toggle('active', btn.dataset.usecase === key);
  });
}

async function openDashboardFromHome() {
  if (state.token) { showApp(); return; }
  try {
    const res = await fetch('/api/me');
    if (res.ok) { showApp(); return; }
  } catch {}
  showAuth();
  setAuthMode('login');
}

function logout() {
  if (state.ws) { state.ws.close(); state.ws = null; }
  state.token = null;
  localStorage.removeItem('ac_token');
  showHome();
}

$('login-form').addEventListener('submit', async e => {
  e.preventDefault();
  const btn = $('login-btn');
  const errEl = $('auth-error');
  errEl.textContent = '';
  btn.disabled = true;
  btn.textContent = 'Signing in...';
  try {
    const res = await fetch('/api/auth/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        email: $('email').value.trim(),
        password: $('password').value,
      }),
    });
    const data = await res.json();
    if (!res.ok) {
      errEl.textContent = data.error || 'Login failed';
      return;
    }
    state.token = data.token;
    localStorage.setItem('ac_token', data.token);
    showApp();
  } catch (err) {
    errEl.textContent = 'Network error — is the server running?';
  } finally {
    btn.disabled = false;
    btn.textContent = 'Sign in';
  }
});

$('logout-btn').addEventListener('click', logout);

// ── Theme toggle ───────────────────────────────────────────────────────────────
function applyTheme(theme) {
  document.documentElement.setAttribute('data-theme', theme);
  $('theme-btn').textContent = theme === 'light' ? '◑' : '◐';
  localStorage.setItem('ac_theme', theme);
}
(function initTheme() {
  const saved = localStorage.getItem('ac_theme');
  const preferred = window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark';
  applyTheme(saved || preferred);
})();
$('approval-badge').addEventListener('click', toggleApprovals);
$('theme-btn').addEventListener('click', () => {
  const next = document.documentElement.getAttribute('data-theme') === 'light' ? 'dark' : 'light';
  applyTheme(next);
});

$('home-open-dashboard').addEventListener('click', openDashboardFromHome);
$('home-sign-in').addEventListener('click', () => {
  showAuth();
  setAuthMode('login');
  $('email').focus();
});
$('home-register').addEventListener('click', () => {
  showAuth();
  setAuthMode('register');
  $('reg-name').focus();
});
document.querySelectorAll('.usecase-btn').forEach(btn => {
  btn.addEventListener('click', () => renderUseCase(btn.dataset.usecase));
});
renderUseCase('solo');

$('show-register').addEventListener('click', e => {
  e.preventDefault();
  setAuthMode('register');
  $('reg-name').focus();
});

$('show-login').addEventListener('click', e => {
  e.preventDefault();
  setAuthMode('login');
  $('email').focus();
});

$('register-form').addEventListener('submit', async e => {
  e.preventDefault();
  const btn = $('register-btn');
  const errEl = $('reg-error');
  errEl.textContent = '';
  btn.disabled = true;
  btn.textContent = 'Creating...';
  try {
    const res = await fetch('/api/auth/register', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        name: $('reg-name').value.trim(),
        email: $('reg-email').value.trim(),
        password: $('reg-password').value,
      }),
    });
    const data = await res.json();
    if (!res.ok) {
      errEl.textContent = data.error || 'Registration failed';
      return;
    }
    state.token = data.token;
    localStorage.setItem('ac_token', data.token);
    showApp();
  } catch (err) {
    errEl.textContent = 'Network error — is the server running?';
  } finally {
    btn.disabled = false;
    btn.textContent = 'Create account';
  }
});

// ── Data fetching ──────────────────────────────────────────────────────────────

async function fetchAll() {
  const [hosts, sessions, approvals, pending] = await Promise.all([
    api('GET', '/api/hosts'),
    api('GET', '/api/sessions'),
    api('GET', '/api/approvals'),
    api('GET', '/api/device/pending'),
  ]);
  if (hosts    !== null) { state.hosts          = hosts    || []; renderHosts(); }
  if (sessions !== null) { state.sessions       = sessions || []; renderSessions(); }
  if (approvals!== null) { state.approvals      = approvals|| []; renderApprovals(); }
  if (pending  !== null) { state.pendingDevices = pending  || []; renderHosts(); }
}

// Poll for pending device authorizations every 4s so new `agentcockpit install`
// invocations appear in the dashboard without a manual refresh.
setInterval(async () => {
  if (!state.token) return;
  const pending = await api('GET', '/api/device/pending');
  if (pending !== null) {
    const changed = JSON.stringify(pending) !== JSON.stringify(state.pendingDevices);
    state.pendingDevices = pending || [];
    if (changed) {
      renderHosts();
      // Also refresh host list in case an authorization just completed.
      const hosts = await api('GET', '/api/hosts');
      if (hosts !== null) { state.hosts = hosts || []; renderHosts(); }
    }
  }
}, 4000);

// ── WebSocket ──────────────────────────────────────────────────────────────────

function setWS(s) {
  state.wsState = s;
  const dot   = $('ws-dot');
  const label = $('ws-label');
  dot.className = 'ws-dot ' + s;
  label.textContent = s;
}

function connectWS() {
  if (state.ws) return;
  setWS('connecting');

  const proto = location.protocol === 'https:' ? 'wss' : 'ws';
  const ws = new WebSocket(`${proto}://${location.host}/ws/browser?token=${state.token}`);
  ws.binaryType = 'arraybuffer';
  state.ws = ws;

  ws.onopen = () => {
    setWS('connected');
    fetchAll();
  };

  ws.onclose = () => {
    state.ws = null;
    setWS('disconnected');
    // Reconnect after 5s
    setTimeout(() => { if (state.token) connectWS(); }, 5000);
  };

  ws.onerror = () => {
    ws.close();
  };

  ws.onmessage = e => {
    if (e.data instanceof ArrayBuffer) {
      // Binary PTY frame: [0x01][32-byte hex sessionId][PTY data]
      const buf = new Uint8Array(e.data);
      if (buf.length < 33 || buf[0] !== 0x01) return;
      const sessionId = String.fromCharCode(...buf.slice(1, 33));
      if (state.attachedSession && sessionId === state.attachedSession) {
        termWrite(buf.slice(33));
      }
      return;
    }
    let msg;
    try { msg = JSON.parse(e.data); } catch { return; }
    handleWS(msg);
  };
}

function handleWS(msg) {
  switch (msg.type) {
    case 'host_status': {
      const h = state.hosts.find(h => h.ID === msg.hostId);
      if (h) {
        h.Status = msg.status;
        renderHosts();
      } else {
        fetchAll(); // new host appeared
      }
      break;
    }
    case 'approval_request': {
      // Avoid duplicates
      if (!state.approvals.find(a => a.ID === msg.requestId)) {
        state.approvals.unshift({
          ID: msg.requestId,
          SessionID: msg.sessionId,
          ToolName: msg.toolName,
          ToolInput: typeof msg.toolInput === 'string' ? msg.toolInput : JSON.stringify(msg.toolInput, null, 2),
          RiskLevel: msg.riskLevel,
          Status: 'pending',
        });
        renderApprovals();
        toast(`⚠ Approval needed: ${msg.toolName}`);
      }
      break;
    }
    case 'approval_resolved': {
      state.approvals = state.approvals.filter(a => a.ID !== msg.id);
      renderApprovals();
      break;
    }
    case 'session_started': {
      const s = state.sessions.find(s => s.ID === msg.sessionId);
      if (s) { s.Status = 'running'; renderSessions(); }
      else fetchAll();
      break;
    }
    case 'session_stopped': {
      const s = state.sessions.find(s => s.ID === msg.sessionId);
      if (s) { s.Status = 'stopped'; renderSessions(); }
      // Update terminal badge if attached
      if (state.attachedSession === msg.sessionId || $('term-modal').classList.contains('visible')) {
        const sb = $('term-status');
        if (sb.textContent !== 'stopped') {
          sb.textContent = 'stopped'; sb.className = 'status-badge stopped';
          state.attachedSession = null;
          if (state.xterm) state.xterm.options.disableStdin = true;
        }
      }
      break;
    }
    case 'session_tokens': {
      state.sessionTokens[msg.sessionId] = {
        inputTokens: msg.inputTokens,
        contextWindowSize: msg.contextWindowSize,
      };
      renderSessions();
      break;
    }
    case 'session_deleted': {
      state.sessions = state.sessions.filter(s => s.ID !== msg.sessionId);
      renderSessions();
      break;
    }
    case 'host_deleted': {
      state.hosts = state.hosts.filter(h => h.ID !== msg.hostId);
      state.sessions = state.sessions.filter(s => s.HostID !== msg.hostId);
      renderHosts();
      renderSessions();
      break;
    }
  }
}

// ── Approval actions ───────────────────────────────────────────────────────────

async function resolve(id, decision) {
  const card = document.querySelector(`[data-id="${id}"]`);
  if (card) {
    card.querySelectorAll('button').forEach(b => b.disabled = true);
    card.style.opacity = '.5';
  }
  try {
    await api('POST', `/api/approvals/${id}`, { decision });
    state.approvals = state.approvals.filter(a => a.ID !== id);
    renderApprovals();
    toast(decision === 'approved' ? '✓ Allowed' : '✗ Denied');
  } catch (err) {
    if (card) {
      card.querySelectorAll('button').forEach(b => b.disabled = false);
      card.style.opacity = '1';
    }
    toast('Error: ' + err.message);
  }
}

// ── Rendering ──────────────────────────────────────────────────────────────────

function toggleApprovals() {
  const sec = $('approvals-section');
  const open = sec.classList.toggle('open');
  $('approvals-toggle-label').textContent = open ? '▾ collapse' : '▸ expand';
}

function renderApprovals() {
  const list  = $('approvals-list');
  const count = $('approvals-count');
  const badge = $('approval-badge');
  const sec   = $('approvals-section');
  const n = state.approvals.length;

  count.textContent = n;
  $('approval-badge-count').textContent = n;

  if (n > 0) {
    badge.style.display = '';
    // Auto-expand when new approvals arrive
    if (!sec.classList.contains('open')) sec.classList.add('open');
    $('approvals-toggle-label').textContent = '▾ collapse';
  } else {
    badge.style.display = 'none';
    sec.classList.remove('open');
  }

  if (n === 0) {
    list.innerHTML = '';
    return;
  }

  list.innerHTML = state.approvals.map(a => {
    const risk = (a.RiskLevel || 'execute').toLowerCase();
    let inputStr = a.ToolInput || '';
    if (typeof inputStr === 'object') inputStr = JSON.stringify(inputStr, null, 2);
    try {
      const parsed = JSON.parse(inputStr);
      inputStr = JSON.stringify(parsed, null, 2);
    } catch {}
    const preview = inputStr.length > 60 ? inputStr.slice(0, 60) + '…' : inputStr;

    return `
<div class="approval-card risk-${risk}" data-id="${a.ID}">
  <div class="approval-header">
    <span class="risk-badge ${risk}">${risk}</span>
    <span class="tool-name">${esc(a.ToolName)}</span>
  </div>
  <div class="approval-meta">
    <span>session: ${esc((a.SessionID||'').slice(0,12))}…</span>
    &nbsp;·&nbsp;
    <button class="tool-input-toggle" onclick="toggleInput(this)">▸ view input</button>
    <div class="tool-input-body">${esc(inputStr)}</div>
  </div>
  <div class="approval-actions">
    <button class="btn-allow" onclick="resolve('${a.ID}','approved')">✓ Allow</button>
    <button class="btn-deny"  onclick="resolve('${a.ID}','rejected')">✗ Deny</button>
  </div>
</div>`;
  }).join('');
}

function toggleInput(btn) {
  const body = btn.nextElementSibling;
  const open = body.classList.toggle('open');
  btn.textContent = open ? '▾ hide input' : '▸ view input';
}

function renderHosts() {
  const el = $('hosts-list');
  const total = state.hosts.length + state.pendingDevices.length;
  $('hosts-count').textContent = total;

  if (!total) {
    el.innerHTML = '<div class="empty-state">No hosts connected. Click <strong style="color:var(--text)">+ Add</strong> to get started.</div>';
    return;
  }

  const pendingHtml = state.pendingDevices.map(d => `
<div class="host-row">
  <div class="host-row-top">
    <div class="status-dot" style="background:var(--amber);animation:blink 1s infinite"></div>
    <span class="host-name">${esc(d.hostname || 'unknown')}</span>
    <span class="host-platform">${esc(d.platform || '?')}</span>
    <button class="btn-authorize" onclick="authorizeDevice('${esc(d.user_code)}','${esc(d.hostname)}')">Authorize</button>
  </div>
  <div class="host-meta">waiting to connect · run <code>agentcockpit install</code> then click Authorize</div>
</div>`).join('');

  const hostHtml = state.hosts.map(h => {
    const online = h.Status === 'online';
    const lastSeen = h.LastSeenAt ? relTime(h.LastSeenAt) : '—';
    return `
<div class="host-row">
  <div class="host-row-top">
    <div class="status-dot ${online ? 'online' : 'offline'}"></div>
    <span class="host-name">${esc(h.Name || h.Hostname || h.ID.slice(0,8))}</span>
    <span class="host-platform">${esc(h.Platform || '?')}</span>
    <button class="host-remove" onclick="removeHost('${esc(h.ID)}')" title="Remove host">&#x2715;</button>
  </div>
  <div class="host-meta">${esc(h.Hostname || '')}${h.Hostname ? ' · ' : ''}${online ? 'online' : 'last seen ' + lastSeen}</div>
</div>`;
  }).join('');

  el.innerHTML = pendingHtml + hostHtml;
}

async function authorizeDevice(userCode, hostname) {
  const res = await api('POST', '/api/device/authorize', { user_code: userCode, name: hostname });
  if (!res || res.error) { alert('Authorization failed — the request may have expired.'); return; }
  // Refresh both pending and hosts
  const [hosts, pending] = await Promise.all([
    api('GET', '/api/hosts'),
    api('GET', '/api/device/pending'),
  ]);
  if (hosts   !== null) { state.hosts          = hosts   || []; }
  if (pending !== null) { state.pendingDevices = pending || []; }
  renderHosts();
}

async function removeHost(id) {
  if (!confirm('Remove this host? The agent will stop connecting.')) return;
  const res = await api('DELETE', `/api/hosts/${id}`);
  if (res !== null && res?.error) { toast('Failed to remove host'); return; }
  state.hosts = state.hosts.filter(h => h.ID !== id);
  renderHosts();
}

function renderTokenBar(tok) {
  const pct = tok ? Math.min(100, Math.round(tok.inputTokens / tok.contextWindowSize * 100)) : 0;
  const fillClass = pct >= 90 ? 'crit' : pct >= 70 ? 'warn' : '';
  const label = tok
    ? `${fmtK(tok.inputTokens)} / ${fmtK(tok.contextWindowSize)} (${pct}%)`
    : `0 / — (0%)`;
  return `<div class="token-bar-wrap">
  <div class="token-bar-track"><div class="token-bar-fill ${fillClass}" style="width:${pct}%"></div></div>
  <span class="token-bar-label">${label}</span>
</div>`;
}

function fmtK(n) {
  return n >= 1000 ? (n / 1000).toFixed(n >= 10000 ? 0 : 1) + 'k' : String(n);
}

function renderSessions() {
  const el = $('sessions-list');
  const count = $('sessions-count');
  count.textContent = state.sessions.length;

  if (!state.sessions.length) {
    el.innerHTML = '<div class="empty-state">No sessions yet.<br>Click <strong style="color:var(--text)">+ New</strong> to start one.</div>';
    return;
  }

  const hostMap = {};
  state.hosts.forEach(h => { hostMap[h.ID] = h.Name || h.Hostname || h.ID.slice(0,8); });

  el.innerHTML = state.sessions.map(s => {
    const name = s.Name || s.Command || 'session';
    const hostName = hostMap[s.HostID] || (s.HostID || '').slice(0,8);
    const dir = s.WorkingDir || '';
    const shortDir = dir.replace(/^\/Users\/[^/]+/, '~').replace(/^\/home\/[^/]+/, '~');
    const running = s.Status === 'running' || s.Status === 'awaiting_approval';
    const started = s.StartedAt ? relTime(s.StartedAt) : s.CreatedAt ? relTime(s.CreatedAt) : '';

    const actions = `<div class="session-actions">
      ${running
        ? `<button class="btn-session-action live" onclick="openTerminal('${esc(s.ID)}','${esc(name)}','${esc(s.Status)}',true)">attach</button>
           <button class="btn-session-action danger" onclick="killSession('${esc(s.ID)}')">kill</button>`
        : `<button class="btn-session-action" onclick="openTerminal('${esc(s.ID)}','${esc(name)}','${esc(s.Status)}',false)">replay</button>`
      }
      <button class="btn-session-action danger" onclick="deleteSession('${esc(s.ID)}')">delete</button>
    </div>`;

    const tok = state.sessionTokens[s.ID];
    const tokenBar = renderTokenBar(tok);
    return `
<div class="session-row">
  <div class="session-row-top">
    <div class="status-dot ${running ? 'online' : 'offline'}"></div>
    <span class="session-name">${esc(name)}</span>
    <span class="status-badge ${s.Status}">${s.Status}</span>
    <span class="session-time">${esc(started)}</span>
    ${actions}
  </div>
  <div class="session-meta">${esc(s.Command || s.AgentType || '')} · ${esc(hostName)} · ${esc(shortDir)}</div>
  ${tokenBar}
</div>`;
  }).join('');
}

async function killSession(id) {
  if (!confirm('Kill this session?')) return;
  await api('DELETE', `/api/sessions/${id}`);
  const s = state.sessions.find(s => s.ID === id);
  if (s) { s.Status = 'stopped'; renderSessions(); }
}

async function deleteSession(id) {
  if (!confirm('Delete this session? This cannot be undone.')) return;
  await api('DELETE', `/api/sessions/${id}/delete`);
  state.sessions = state.sessions.filter(s => s.ID !== id);
  renderSessions();
}

// ── Utilities ──────────────────────────────────────────────────────────────────

// ── Add Host Modal ─────────────────────────────────────────────────────────────

let _modalPollTimer = null;

async function openAddHostModal() {
  $('add-host-modal').classList.add('visible');
  $('modal-body').innerHTML = '<div class="modal-step"><div class="modal-step-label" style="color:var(--dim)">Generating secure invite...</div></div>';

  try {
    const res = await fetch('/api/hosts/invite', {
      method: 'POST',
      headers: { 'Authorization': `Bearer ${state.token}` },
    });
    if (!res.ok) throw new Error('invite failed');
    const { token } = await res.json();
    const cmd = `curl -fsSL https://agentcockpit.sh | sh && agentcockpit install --invite ${token}`;
    const hostCountBefore = state.hosts.length;

    $('modal-body').innerHTML = `
<div class="modal-step">
  <div class="modal-step-label">Run this command on your machine</div>
  <div class="setup-cmd" style="flex-direction:column;gap:6px;align-items:stretch;">
    <code style="word-break:break-all;line-height:1.7">${esc(cmd)}</code>
    <button class="copy-btn" style="align-self:flex-end;" data-cmd="${esc(cmd)}" onclick="copyCmd(this,this.dataset.cmd)">copy</button>
  </div>
  <p style="font-size:11px;color:var(--dim);line-height:1.6;">
    Installs the agent and connects this machine automatically.<br>
    Invite expires in <strong style="color:var(--text)">15 minutes</strong>.
  </p>
</div>
<hr class="modal-divider">
<div id="modal-waiting" style="text-align:center;font-size:11px;color:var(--dim);padding:4px 0;">
  <span style="animation:blink 1.2s infinite;display:inline-block">&#9679;</span>&nbsp; Waiting for host to connect...
</div>`;

    // Poll until a new host appears
    _modalPollTimer = setInterval(async () => {
      const hosts = await api('GET', '/api/hosts');
      if (hosts && hosts.length > hostCountBefore) {
        clearInterval(_modalPollTimer);
        const newHost = hosts.find(h => !state.hosts.find(old => old.ID === h.ID));
        state.hosts = hosts;
        renderHosts();
        $('modal-body').innerHTML = `
<div class="modal-success">
  <div class="modal-success-icon">&#x2713;</div>
  <div class="modal-success-text">${esc(newHost ? (newHost.Name || newHost.Hostname) : 'Host')} connected</div>
  <div class="modal-success-sub">Daemon setup complete. Use <code style="color:var(--green)">agentcockpit status</code> to verify it is online.</div>
</div>`;
        setTimeout(closeAddHostModal, 3000);
      }
    }, 3000);
  } catch {
    $('modal-body').innerHTML = '<div class="modal-step"><div class="modal-step-label" style="color:var(--red)">Failed to generate invite. Please try again.</div></div>';
  }
}

function closeAddHostModal() {
  clearInterval(_modalPollTimer);
  $('add-host-modal').classList.remove('visible');
}

$('add-host-btn').addEventListener('click', openAddHostModal);
$('modal-close-btn').addEventListener('click', closeAddHostModal);
$('add-host-modal').addEventListener('click', e => { if (e.target === $('add-host-modal')) closeAddHostModal(); });
document.addEventListener('keydown', e => { if (e.key === 'Escape') closeAddHostModal(); });

function copyCmd(btn, text) {
  navigator.clipboard.writeText(text).then(() => {
    const orig = btn.textContent;
    btn.textContent = 'copied!';
    btn.style.color = 'var(--green)';
    setTimeout(() => { btn.textContent = orig; btn.style.color = ''; }, 1500);
  });
}

function esc(s) {
  return String(s ?? '')
    .replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;')
    .replace(/"/g,'&quot;');
}

function relTime(iso) {
  const diff = Date.now() - new Date(iso).getTime();
  const s = Math.floor(diff / 1000);
  if (s < 60)  return `${s}s ago`;
  if (s < 3600) return `${Math.floor(s/60)}m ago`;
  return `${Math.floor(s/3600)}h ago`;
}

function toast(msg) {
  const el = document.createElement('div');
  el.className = 'toast';
  el.textContent = msg;
  $('toast-container').appendChild(el);
  setTimeout(() => el.remove(), 3200);
}

// ── Terminal state & functions ─────────────────────────────────────────────────

Object.assign(state, { attachedSession: null, xterm: null, fitAddon: null });

async function openTerminal(sessionId, name, status, live) {
  if (state.xterm) { state.xterm.dispose(); state.xterm = null; state.fitAddon = null; }
  state.attachedSession = live ? sessionId : null;

  $('term-title').textContent = name || sessionId.slice(0, 12);
  const sb = $('term-status');
  sb.textContent = status;
  sb.className = `status-badge ${status}`;

  const term = new Terminal({
    cursorBlink: true,
    fontSize: 13,
    fontFamily: '"MesloLGS NF","FiraCode Nerd Font Mono","JetBrainsMono Nerd Font Mono","Symbols Nerd Font Mono","Source Code Pro","SF Mono","Fira Code","Cascadia Code",Menlo,monospace',
    theme: { background: '#0d0d0d', foreground: '#d4d4d4', cursor: '#00d67c' },
    scrollback: 5000,
    convertEol: false,
    disableStdin: !live,
  });
  const fitAddon = new FitAddon.FitAddon();
  term.loadAddon(fitAddon);

  const container = $('xterm-container');
  container.innerHTML = '';
  term.open(container);

  state.xterm = term;
  state.fitAddon = fitAddon;

  if (live) {
    // Keyboard input → PTY stdin
    term.onData(data => {
      if (!state.ws || state.wsState !== 'connected' || !state.attachedSession) return;
      const bytes = new TextEncoder().encode(data);
      const b64 = btoa(Array.from(bytes, b => String.fromCharCode(b)).join(''));
      state.ws.send(JSON.stringify({ type: 'stdin_data', sessionId: state.attachedSession, data: b64 }));
    });
    // Notify agent of terminal dimensions on resize
    term.onResize(({ cols, rows }) => {
      if (!state.ws || state.wsState !== 'connected' || !state.attachedSession) return;
      state.ws.send(JSON.stringify({ type: 'session_resize', sessionId: state.attachedSession, cols, rows }));
    });
  }

  $('term-modal').classList.add('visible');
  // Double rAF: first frame resolves display:flex layout, second measures stable dimensions
  requestAnimationFrame(() => requestAnimationFrame(() => { fitAddon.fit(); if (live) term.focus(); }));

  // Replay stored scrollback
  try {
    const events = await api('GET', `/api/sessions/${sessionId}/events`);
    if (events && state.xterm) {
      for (const ev of events) {
        if (ev.Type === 'output' && ev.Data) {
          state.xterm.write(Uint8Array.from(atob(ev.Data), c => c.charCodeAt(0)));
        }
      }
    }
  } catch {}
}

function detachSession() {
  state.attachedSession = null;
  if (state.xterm) { state.xterm.dispose(); state.xterm = null; state.fitAddon = null; }
  $('term-modal').classList.remove('visible');
}

// Called by the WS handler when a binary PTY frame arrives.
function termWrite(data) {
  if (state.xterm) state.xterm.write(data);
}

// ── New Session Modal ──────────────────────────────────────────────────────────

function openNewSessionModal() {
  const onlineHosts = state.hosts.filter(h => h.Status === 'online');
  const sel = $('ns-host');
  sel.innerHTML = onlineHosts.length
    ? onlineHosts.map(h => `<option value="${esc(h.ID)}">${esc(h.Name || h.Hostname || h.ID.slice(0,8))}</option>`).join('')
    : '<option value="">No hosts online</option>';
  $('ns-error').textContent = '';
  $('new-session-modal').classList.add('visible');
}

function closeNewSessionModal() {
  $('new-session-modal').classList.remove('visible');
}

$('new-session-btn').addEventListener('click', openNewSessionModal);
$('ns-close-btn').addEventListener('click', closeNewSessionModal);
$('new-session-modal').addEventListener('click', e => { if (e.target === $('new-session-modal')) closeNewSessionModal(); });

$('ns-start-btn').addEventListener('click', async () => {
  const hostId = $('ns-host').value;
  const errEl  = $('ns-error');
  errEl.textContent = '';

  if (!hostId) { errEl.textContent = 'Select a host.'; return; }

  const btn = $('ns-start-btn');
  btn.disabled = true; btn.textContent = 'Starting…';
  try {
    const sess = await api('POST', '/api/sessions', { host_id: hostId });
    if (sess && sess.ID) {
      state.sessions.unshift(sess);
      renderSessions();
      closeNewSessionModal();
      setTimeout(() => openTerminal(sess.ID, sess.Name || 'shell', 'starting', true), 600);
    }
  } catch (err) {
    errEl.textContent = err.message || 'Failed to start session.';
  } finally {
    btn.disabled = false; btn.textContent = 'Start shell';
  }
});

// ── Terminal event listeners ───────────────────────────────────────────────────

$('term-close-btn').addEventListener('click', detachSession);
document.addEventListener('keydown', e => {
  if (e.key === 'Escape' && $('new-session-modal').classList.contains('visible')) closeNewSessionModal();
});
window.addEventListener('resize', () => { if (state.fitAddon) state.fitAddon.fit(); });

// ── Init ───────────────────────────────────────────────────────────────────────

(async function initByRoute() {
  if (currentPath === '/dashboard') {
    if (state.token) { showApp(); return; }
    try {
      const res = await fetch('/api/me');
      if (res.ok) { showApp(); return; }
    } catch {}
    location.href = '/login';
    return;
  }
  if (currentPath === '/login') {
    if (state.token) { location.href = '/dashboard'; return; }
    setAuthMode('login');
    showAuth();
    return;
  }
  if (state.token) {
    showHome();
    return;
  }
  setAuthMode('login');
  showHome();
})();
