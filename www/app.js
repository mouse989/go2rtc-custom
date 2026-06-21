/* ── go2rtc shared frontend logic ── */

// ─────────────────────────── Helpers ───────────────────────────
function escHtml(s) {
  return String(s).replace(/[&<>"']/g, c =>
    ({ '&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;' }[c]));
}

function toast(msg, type = 'info') {
  let container = document.getElementById('toastContainer');
  if (!container) {
    container = document.createElement('div');
    container.id = 'toastContainer';
    container.className = 'toast-container';
    document.body.appendChild(container);
  }
  const t = document.createElement('div');
  t.className = 'toast ' + type;
  t.textContent = msg;
  container.appendChild(t);
  setTimeout(() => t.remove(), 3500);
}

// ─────────────────────────── Auth helpers ───────────────────────────
function getToken() {
  return localStorage.getItem('go2rtc_token') || '';
}

function getUser() {
  try {
    return JSON.parse(localStorage.getItem('go2rtc_user') || 'null');
  } catch { return null; }
}

function isAdmin() {
  const u = getUser();
  return u && u.role === 'admin';
}

function canUseTraffic() {
  const u = getUser();
  return u && (u.role === 'admin' || !!u.allow_traffic);
}

function canUseHeatmap() {
  const u = getUser();
  return u && (u.role === 'admin' || !!u.allow_heatmap);
}

function canEditMapLocations() {
  const u = getUser();
  return u && (u.role === 'admin' || !!u.allow_map_edit);
}

function canSeeCamNames() {
  const u = getUser();
  return u && (u.role === 'admin' || !!u.allow_cam_names);
}

function canViewStations() {
  const u = getUser();
  return u && (u.role === 'admin' || !!u.allow_view_stations || !!u.allow_config_stations);
}

function canConfigStations() {
  const u = getUser();
  return u && (u.role === 'admin' || !!u.allow_config_stations);
}

// hasTab returns true if the current user has permission for the given page tab.
// Admin always returns true; viewer checks the tabs array from /api/auth/me.
function hasTab(tab) {
  const u = getUser();
  if (!u) return false;
  if (u.role === 'admin') return true;
  return (u.tabs || []).includes(tab);
}

async function apiFetch(path, opts = {}) {
  const token = getToken();
  const headers = Object.assign({}, opts.headers || {});
  if (token) headers['Authorization'] = 'Bearer ' + token;
  if (opts.body && typeof opts.body === 'object' && !(opts.body instanceof FormData)) {
    headers['Content-Type'] = 'application/json';
    opts = { ...opts, body: JSON.stringify(opts.body) };
  }
  try {
    const res = await fetch(path, { ...opts, headers });
    if (res.status === 401) {
      localStorage.removeItem('go2rtc_token');
      localStorage.removeItem('go2rtc_user');
      window.location.href = '/login.html';
      return null;
    }
    if (!res.ok) {
      const txt = await res.text().catch(() => '');
      throw new Error(txt || res.statusText);
    }
    const ct = res.headers.get('content-type') || '';
    if (res.status === 204) return null;
    return ct.includes('application/json') ? res.json() : res.text();
  } catch (err) {
    if (err.message !== 'redirect') console.error('[api]', path, err);
    throw err;
  }
}

// ─────────────────────────── initApp ───────────────────────────
// Call this at the top of every protected page.
// Redirects to /login.html if not authenticated.
// ─────────────────────────── Clipboard ───────────────────────────
// Works on HTTP (not just HTTPS) via execCommand fallback.
function copyText(text, btn) {
  if (!text) return;
  const orig = btn ? btn.textContent : '';
  const done = () => { if (btn) { btn.textContent = '✓ Copied!'; setTimeout(() => { btn.textContent = orig; }, 1800); } };
  const fail = () => { try { const ta = Object.assign(document.createElement('textarea'), { value: text }); Object.assign(ta.style, { position:'fixed', left:'-9999px', top:'0', opacity:'0' }); document.body.appendChild(ta); ta.focus(); ta.select(); document.execCommand('copy'); document.body.removeChild(ta); done(); } catch(_) {} };
  if (navigator.clipboard && navigator.clipboard.writeText) {
    navigator.clipboard.writeText(text).then(done).catch(fail);
  } else { fail(); }
}

// Returns the first page the user is permitted to access.
function firstPermittedPage(u) {
  if (!u || u.role === 'admin') return '/';
  const order = [
    { tab: 'cameras',   page: '/' },
    { tab: 'map',       page: '/map.html' },
    { tab: 'dashboard', page: '/dashboard.html' },
    { tab: 'monitor',   page: '/monitor.html' },
    { tab: 'log',       page: '/log.html' },
    { tab: 'config',    page: '/config.html' },
    { tab: 'api_docs',  page: '/api-docs.html' },
    { tab: 'counting',  page: '/counting.html' },
  ];
  for (const { tab, page } of order) {
    if ((u.tabs || []).includes(tab)) return page;
  }
  return '/login.html?err=no_access';
}

// initApp(requiredTab?) — call at top of every protected page.
// requiredTab: the tab key this page requires (e.g. 'cameras', 'map').
// If the user lacks that tab they are redirected to their first permitted page.
async function initApp(requiredTab) {
  const token = getToken();

  // Try to refresh user info from server
  if (token) {
    try {
      const me = await fetch('/api/auth/me', {
        headers: { 'Authorization': 'Bearer ' + token }
      });
      if (me.ok) {
        const data = await me.json();
        localStorage.setItem('go2rtc_user', JSON.stringify(data));
      } else if (me.status === 401) {
        localStorage.removeItem('go2rtc_token');
        localStorage.removeItem('go2rtc_user');
        window.location.href = '/login.html';
        return;
      }
    } catch {
      // offline / server not started — use cached user info if available
      if (!getUser()) {
        window.location.href = '/login.html';
        return;
      }
    }
  } else {
    window.location.href = '/login.html';
    return;
  }

  // Tab guard: redirect viewer to their first permitted page if they lack access.
  if (requiredTab) {
    const u = getUser();
    if (u && u.role !== 'admin' && !(u.tabs || []).includes(requiredTab)) {
      window.location.href = firstPermittedPage(u);
      return;
    }
  }

  // Populate sidebar UI
  const user = getUser();
  if (user) {
    const nameEl   = document.getElementById('userName');
    const roleEl   = document.getElementById('userRole');
    const avatarEl = document.getElementById('avatar');
    if (nameEl)   nameEl.textContent   = user.username;
    if (roleEl)   roleEl.textContent   = user.role;
    if (avatarEl) avatarEl.textContent = user.username.charAt(0).toUpperCase();
  }

  // Show admin-only nav items
  if (isAdmin()) document.body.classList.add('is-admin');

  // Show tab-gated nav items for users who have those tabs
  document.querySelectorAll('[data-tab-require]').forEach(el => {
    if (hasTab(el.dataset.tabRequire)) el.style.display = '';
  });

  // ── Sidebar toggle ─────────────────────────────────────────────
  // Desktop: sb-collapsed body class (icon-only collapse via CSS var)
  // Mobile:  .open class + backdrop overlay; close on backdrop tap
  const toggleBtn = document.getElementById('sidebarToggle');
  const sidebar   = document.getElementById('sidebar');

  // Inject backdrop element once (shared across all pages via app.js)
  let backdrop = document.getElementById('sidebarBackdrop');
  if (!backdrop) {
    backdrop = document.createElement('div');
    backdrop.id = 'sidebarBackdrop';
    document.body.appendChild(backdrop);
  }

  function closeMobileSidebar() {
    sidebar.classList.remove('open');
    backdrop.classList.remove('visible');
    if (window.__map) setTimeout(() => window.__map.invalidateSize(), 260);
  }

  if (toggleBtn && sidebar) {
    toggleBtn.addEventListener('click', () => {
      if (window.innerWidth <= 768) {
        const opening = !sidebar.classList.contains('open');
        sidebar.classList.toggle('open');
        backdrop.classList.toggle('visible', opening);
        if (window.__map && !opening) setTimeout(() => window.__map.invalidateSize(), 260);
      } else {
        document.body.classList.toggle('sb-collapsed');
        if (window.__map) setTimeout(() => window.__map.invalidateSize(), 220);
      }
    });
    backdrop.addEventListener('click', closeMobileSidebar);
  }

  // ── Theme toggle (🌙 / ☀️ button in sidebar footer) ─────────────
  const SUN_SVG  = `<svg viewBox="0 0 20 20" fill="currentColor" width="16" height="16"><path fill-rule="evenodd" d="M10 2a1 1 0 011 1v1a1 1 0 11-2 0V3a1 1 0 011-1zm4 8a4 4 0 11-8 0 4 4 0 018 0zm-.464 4.95l.707.707a1 1 0 001.414-1.414l-.707-.707a1 1 0 00-1.414 1.414zm2.12-10.607a1 1 0 010 1.414l-.706.707a1 1 0 11-1.414-1.414l.707-.707a1 1 0 011.414 0zM17 11a1 1 0 100-2h-1a1 1 0 100 2h1zm-7 4a1 1 0 011 1v1a1 1 0 11-2 0v-1a1 1 0 011-1zM5.05 6.464A1 1 0 106.465 5.05l-.708-.707a1 1 0 00-1.414 1.414l.707.707zm1.414 8.486l-.707.707a1 1 0 01-1.414-1.414l.707-.707a1 1 0 011.414 1.414zM4 11a1 1 0 100-2H3a1 1 0 000 2h1z" clip-rule="evenodd"/></svg>`;
  const MOON_SVG = `<svg viewBox="0 0 20 20" fill="currentColor" width="16" height="16"><path d="M17.293 13.293A8 8 0 016.707 2.707a8.001 8.001 0 1010.586 10.586z"/></svg>`;

  // Apply saved preference before render to avoid flash
  const savedTheme = localStorage.getItem('utmc_theme');
  if (savedTheme === 'light') document.body.classList.add('light-theme');

  const sidebarFooter = document.querySelector('.sidebar-footer');
  if (sidebarFooter) {
    const themeBtn = document.createElement('button');
    themeBtn.id    = 'btnTheme';
    themeBtn.className = 'btn-logout';
    themeBtn.title = 'Toggle light / dark theme';
    const updateIcon = () => {
      themeBtn.innerHTML = document.body.classList.contains('light-theme') ? MOON_SVG : SUN_SVG;
    };
    updateIcon();
    themeBtn.addEventListener('click', () => {
      document.body.classList.toggle('light-theme');
      localStorage.setItem('utmc_theme', document.body.classList.contains('light-theme') ? 'light' : 'dark');
      updateIcon();
    });
    // Insert before the logout button
    const logoutBtn2 = sidebarFooter.querySelector('#btnLogout');
    if (logoutBtn2) sidebarFooter.insertBefore(themeBtn, logoutBtn2);
    else sidebarFooter.appendChild(themeBtn);
  }

  // ── Logout ─────────────────────────────────────────────────────
  const logoutBtn = document.getElementById('btnLogout');
  if (logoutBtn) {
    logoutBtn.addEventListener('click', async () => {
      await fetch('/api/auth/logout', { method: 'POST' });
      localStorage.removeItem('go2rtc_token');
      localStorage.removeItem('go2rtc_user');
      window.location.href = '/login.html';
    });
  }
}
