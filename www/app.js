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

// Mirrors internal/auth/password_policy.go's validatePassword (\p{L}/\p{Nd}
// so accented Vietnamese letters count as letters, not "special", same as
// Go's unicode.IsLetter/IsDigit) — checked client-side too so the user sees
// the rule immediately instead of only after a round trip to the server.
// Returns '' when pw is valid, or a Vietnamese message to show otherwise.
function passwordPolicyError(pw) {
  const msg = 'Mật khẩu phải có ít nhất 8 ký tự, gồm 1 chữ số và 1 ký tự đặc biệt.';
  if (pw.length < 8) return msg;
  const hasDigit = /\p{Nd}/u.test(pw);
  const hasSpecial = /[^\p{L}\p{Nd}]/u.test(pw);
  return (hasDigit && hasSpecial) ? '' : msg;
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

// canUseTrafficLive gates the alternative, higher-quality live traffic
// source (VietMap Live Traffic Tile) and the on-map toggle to pick it over
// the existing traffic overlay — independent of canUseTraffic().
function canUseTrafficLive() {
  const u = getUser();
  return u && (u.role === 'admin' || !!u.allow_traffic_live);
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

function canCamSnapshot() { const u = getUser(); return u && (u.role === 'admin' || !!u.allow_cam_snapshot); }
function canCamVideo()    { const u = getUser(); return u && (u.role === 'admin' || !!u.allow_cam_video); }
function canMapSnapshotPreview() { const u = getUser(); return u && (u.role === 'admin' || !!u.allow_map_snapshot_preview); }
function canAIAnalyze() { const u = getUser(); return u && (u.role === 'admin' || !!u.allow_ai_analyze); }
function canAIWatch() { const u = getUser(); return u && (u.role === 'admin' || !!u.allow_ai_watch); }
function canMapSearch() { const u = getUser(); return u && (u.role === 'admin' || !!u.allow_map_search); }
function canMapRoute()  { const u = getUser(); return u && (u.role === 'admin' || !!u.allow_map_route); }
// Requires both the permission flag AND the Incidents tab — the flag alone
// would show the map button to someone who can't actually create incidents
// (POST /api/incidents requires the Incidents tab server-side regardless).
function canMapIncidentAdd() { const u = getUser(); return u && (u.role === 'admin' || !!u.allow_map_incident_add) && hasTab('incidents'); }
function canMapAIEvent() { const u = getUser(); return u && (u.role === 'admin' || !!u.allow_map_ai_events); }
function canMap3D() { const u = getUser(); return u && (u.role === 'admin' || !!u.allow_map_3d); }
function canMapTrafficFlow() { const u = getUser(); return u && (u.role === 'admin' || !!u.allow_map_traffic_flow); }
function canMapGIS() { const u = getUser(); return u && (u.role === 'admin' || !!u.allow_map_gis); }
function canMonitorWorkers()   { const u = getUser(); return u && (u.role === 'admin' || !!u.allow_monitor_workers); }
function canMonitorProcess()   { const u = getUser(); return u && (u.role === 'admin' || !!u.allow_monitor_process); }
function canMonitorStreaming()  { const u = getUser(); return u && (u.role === 'admin' || !!u.allow_monitor_streaming); }
function canMonitorSnapshot()  { const u = getUser(); return u && (u.role === 'admin' || !!u.allow_monitor_snapshot); }
function canMonitorDevices()   { const u = getUser(); return u && (u.role === 'admin' || !!u.allow_monitor_devices); }

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

// ─────────────────── Device binding (WebAuthn step-up) ───────────────────
// Some sensitive-data categories (see internal/auth/device_binding.go's
// scope catalog) can be gated per-user by an admin: RequiresDeviceBindingFor
// scope X means the server refuses to serve X unless the browser also
// carries a short-lived "device verified" cookie, proven by a WebAuthn
// assertion against a device an admin previously approved. One verification
// satisfies every gated scope for the life of that cookie
// (device_verify_valid_hours, default 12h) — the server tracks that; this
// file only needs to know whether /api/auth/me's cached device_verified
// flag is currently true.

function deviceBindingScopes() {
  const u = getUser();
  return (u && u.device_binding_scopes) || [];
}

// needsDeviceVerification reports whether scope applies to the current user
// and hasn't been satisfied yet this session. Admins are never gated.
function needsDeviceVerification(scope) {
  const u = getUser();
  if (!u || u.role === 'admin') return false;
  return deviceBindingScopes().includes(scope) && !u.device_verified;
}

function webauthnSupported() {
  return !!(window.PublicKeyCredential && PublicKeyCredential.parseCreationOptionsFromJSON && PublicKeyCredential.parseRequestOptionsFromJSON);
}

// A friendlier default than navigator.platform (which reports e.g. "MacIntel"
// for an iPhone/iPad under Safari's UA string). Just a starting point the
// user can edit — doesn't need to be exact.
function _guessDeviceLabel() {
  const ua = navigator.userAgent || '';
  let device = 'Thiết bị';
  if (/iPhone/.test(ua)) device = 'iPhone';
  else if (/iPad/.test(ua)) device = 'iPad';
  else if (/Android/.test(ua)) device = 'Android';
  else if (/Windows/.test(ua)) device = 'Windows';
  else if (/Macintosh/.test(ua)) device = 'Mac';
  else if (/Linux/.test(ua)) device = 'Linux';
  let browser = '';
  if (/EdgA|Edge|Edg\//.test(ua)) browser = 'Edge';
  else if (/CriOS|Chrome/.test(ua)) browser = 'Chrome';
  else if (/FxiOS|Firefox/.test(ua)) browser = 'Firefox';
  else if (/Safari/.test(ua)) browser = 'Safari';
  return browser ? `${device} (${browser})` : device;
}

let _deviceModalPromise = null; // in-flight Promise, so concurrent callers share one dialog
let _deviceModalResolve = null;

function _deviceModalEls() {
  let backdrop = document.getElementById('deviceVerifyBackdrop');
  if (backdrop) return backdrop;

  backdrop = document.createElement('div');
  backdrop.className = 'modal-backdrop';
  backdrop.id = 'deviceVerifyBackdrop';
  backdrop.innerHTML = `
    <div class="modal" style="max-width:420px">
      <div class="modal-header">
        <h3>Xác minh thiết bị</h3>
        <button class="modal-close" id="devVerifyClose" type="button">✕</button>
      </div>
      <div class="modal-body">
        <p style="font-size:.85rem;color:var(--text-muted);margin-bottom:.8rem">
          Dữ liệu này yêu cầu xác minh thiết bị đã được quản trị viên phê duyệt.
        </p>
        <div id="devVerifyErr" style="color:var(--red);font-size:.8rem;display:none;margin-bottom:.6rem"></div>
        <div style="display:flex;flex-direction:column;gap:.6rem">
          <button class="btn btn-primary" id="devVerifyBtn" type="button">🔐 Xác minh bằng thiết bị này</button>
          <div style="border-top:1px solid var(--border);padding-top:.6rem;margin-top:.2rem">
            <label style="font-size:.78rem;color:var(--text-muted);display:block;margin-bottom:.35rem">Tên thiết bị (để quản trị viên dễ nhận biết khi duyệt)</label>
            <input type="text" id="devLabelInput" class="input" style="margin-bottom:.5rem" maxlength="80">
            <button class="btn btn-secondary" id="devRegisterBtn" type="button" style="width:100%">➕ Đăng ký thiết bị mới (chờ duyệt)</button>
          </div>
        </div>
      </div>
    </div>`;
  document.body.appendChild(backdrop);

  const closeCancelled = () => _resolveDeviceModal(false);
  document.getElementById('devVerifyClose').addEventListener('click', closeCancelled);
  backdrop.addEventListener('click', e => { if (e.target === backdrop) closeCancelled(); });
  document.getElementById('devVerifyBtn').addEventListener('click', _deviceVerifyAttempt);
  document.getElementById('devRegisterBtn').addEventListener('click', _deviceRegisterAttempt);
  return backdrop;
}

function _showDeviceModalErr(msg) {
  const el = document.getElementById('devVerifyErr');
  el.textContent = msg;
  el.style.display = '';
}

function _resolveDeviceModal(result) {
  const backdrop = document.getElementById('deviceVerifyBackdrop');
  if (backdrop) backdrop.classList.remove('open');
  const resolve = _deviceModalResolve;
  _deviceModalPromise = null;
  _deviceModalResolve = null;
  if (resolve) resolve(result);
}

async function _deviceVerifyAttempt() {
  document.getElementById('devVerifyErr').style.display = 'none';
  try {
    const options = await apiFetch('/api/auth/webauthn/verify/begin', { method: 'POST' });
    const publicKey = PublicKeyCredential.parseRequestOptionsFromJSON(options.publicKey);
    const cred = await navigator.credentials.get({ publicKey });
    await apiFetch('/api/auth/webauthn/verify/finish', { method: 'POST', body: cred.toJSON() });
    const me = await apiFetch('/api/auth/me');
    localStorage.setItem('go2rtc_user', JSON.stringify(me));
    toast('Đã xác minh thiết bị.', 'success');
    _resolveDeviceModal(true);
  } catch (e) {
    _showDeviceModalErr('Xác minh thất bại: ' + (e.message || e) + ' — nếu thiết bị này chưa được đăng ký, dùng nút bên dưới.');
  }
}

async function _deviceRegisterAttempt() {
  document.getElementById('devVerifyErr').style.display = 'none';
  // An inline input, not prompt(): on Safari/iOS, navigator.credentials.create()
  // throws "NotAllowedError: The document is not focused" if a native
  // dialog (prompt/confirm/alert) ran right before it — the native dialog
  // steals focus and WebKit refuses the ceremony afterwards, failing
  // registration regardless of what name was entered.
  const label = document.getElementById('devLabelInput').value.trim() || _guessDeviceLabel();
  try {
    const options = await apiFetch('/api/auth/webauthn/register/begin', { method: 'POST' });
    const publicKey = PublicKeyCredential.parseCreationOptionsFromJSON(options.publicKey);
    const cred = await navigator.credentials.create({ publicKey });
    await apiFetch('/api/auth/webauthn/register/finish?label=' + encodeURIComponent(label || 'Thiết bị của tôi'), {
      method: 'POST',
      body: cred.toJSON(),
    });
    toast('Đã gửi yêu cầu đăng ký thiết bị — chờ quản trị viên phê duyệt trước khi dùng được.', 'success');
    _resolveDeviceModal(false); // registering alone doesn't grant access yet — still not verified
  } catch (e) {
    _showDeviceModalErr('Đăng ký thất bại: ' + (e.message || e));
  }
}

// ensureDeviceVerified(scope) resolves true immediately if scope doesn't
// gate this user or is already verified; otherwise shows the step-up
// dialog and resolves true/false based on what the user does.
async function ensureDeviceVerified(scope) {
  if (!needsDeviceVerification(scope)) return true;
  if (!webauthnSupported()) {
    toast('Trình duyệt này không hỗ trợ xác minh thiết bị (WebAuthn) — không thể xem dữ liệu này.', 'error');
    return false;
  }
  if (_deviceModalPromise) return _deviceModalPromise;
  const backdrop = _deviceModalEls();
  document.getElementById('devVerifyErr').style.display = 'none';
  const labelInput = document.getElementById('devLabelInput');
  if (labelInput) labelInput.value = _guessDeviceLabel();
  backdrop.classList.add('open');
  _deviceModalPromise = new Promise(resolve => { _deviceModalResolve = resolve; });
  return _deviceModalPromise;
}

// ──────────────────── Login-location capture ────────────────────
// Best-effort, opt-in, once per login: login.html sets go2rtc_geo_pending
// right after a successful sign-in (without waiting on it, so it never
// delays the post-login redirect); the next authenticated page load here
// consumes that flag and asks the browser for a GPS fix exactly once for
// that login. If geolocation is unsupported, blocked by the browser, or
// the user denies the permission prompt, this silently does nothing — no
// retries, no nagging on future page loads until the next fresh login.
function maybeCaptureLoginLocation() {
  if (localStorage.getItem('go2rtc_geo_pending') !== '1') return;
  localStorage.removeItem('go2rtc_geo_pending');
  if (!('geolocation' in navigator)) return;
  navigator.geolocation.getCurrentPosition(
    pos => {
      apiFetch('/api/user-location', {
        method: 'POST',
        body: {
          lat: pos.coords.latitude,
          lon: pos.coords.longitude,
          accuracy: pos.coords.accuracy || 0,
        },
      }).catch(() => {});
    },
    () => {}, // denied / unavailable / timed out — nothing to do
    { enableHighAccuracy: false, timeout: 10000, maximumAge: 60000 }
  );
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

// ──────────────── JamFactor (JF) color scale ────────────────
// Shared across every map that renders JF-colored points (map.html,
// dashboard2.html, trafficinsight.html, …) so a given JF value reads the
// same color everywhere, and an admin customizing the scale (Admin
// Config → 🎨 Thang màu JamFactor) changes it everywhere at once instead
// of each page carrying its own hardcoded thresholds.
//
// JF_COLOR_FALLBACK mirrors internal/auth/jam_color.go's
// jamColorDefaultStops — used only until loadJamColorStops() resolves (or
// if that fetch fails), so a page's first paint isn't blocked on it.
const JF_COLOR_FALLBACK = [
  { value: 0.0, color: '#2ecc71' },
  { value: 3.0, color: '#a3e635' },
  { value: 5.0, color: '#facc15' },
  { value: 6.5, color: '#fb923c' },
  { value: 7.5, color: '#f87171' },
  { value: 8.0, color: '#ef4444' },
  { value: 8.2, color: '#e03e3e' },
  { value: 8.4, color: '#dc2626' },
  { value: 8.6, color: '#c81e1e' },
  { value: 8.8, color: '#b91c1c' },
  { value: 9.0, color: '#a51c1c' },
  { value: 9.2, color: '#941818' },
  { value: 9.4, color: '#841414' },
  { value: 9.6, color: '#711111' },
  { value: 9.8, color: '#5e0d0d' },
  { value: 10.0, color: '#450a0a' },
];

let _jfColorStops = null; // null = not loaded yet; falls back to JF_COLOR_FALLBACK

// loadJamColorStops fetches the admin-configured (or default) JF color
// scale once. Call it before building a map layer or rendering a legend
// that uses jfColorExpression()/jfToColor() — both work off
// JF_COLOR_FALLBACK until this resolves, so calling it isn't strictly
// required, just needed to reflect an admin's customization.
async function loadJamColorStops() {
  try {
    const data = await apiFetch('/api/jam-color-cfg');
    if (data && Array.isArray(data.stops) && data.stops.length >= 2) {
      _jfColorStops = data.stops;
    }
  } catch (_) { /* keep the fallback */ }
  return _jfColorStops || JF_COLOR_FALLBACK;
}

function jfColorStops() {
  return (_jfColorStops && _jfColorStops.length >= 2) ? _jfColorStops : JF_COLOR_FALLBACK;
}

// jfGradientCssString builds a CSS linear-gradient() string spanning the
// stops' own 0–10 value range, for a plain <div> legend bar — the visual
// counterpart to jfColorExpression()'s GL paint expression, so the legend
// always matches what the map itself is drawing.
function jfGradientCssString() {
  const stops = jfColorStops();
  const min = stops[0].value, max = stops[stops.length - 1].value;
  const span = max - min || 1;
  const parts = stops.map(s => `${s.color} ${((s.value - min) / span * 100).toFixed(1)}%`);
  return `linear-gradient(to right, ${parts.join(', ')})`;
}

// jfColorExpression returns a MapLibre/VietMapGL `interpolate` paint
// expression reading the feature's "jf" property — drop this straight
// into a layer's `circle-color` (or similar) paint property so the color
// blends continuously between stops instead of snapping at a threshold.
function jfColorExpression() {
  const stops = jfColorStops();
  const expr = ['interpolate', ['linear'], ['get', 'jf']];
  stops.forEach(s => { expr.push(s.value, s.color); });
  return expr;
}

function _hexToRgb(hex) {
  const n = parseInt(hex.slice(1), 16);
  return [(n >> 16) & 255, (n >> 8) & 255, n & 255];
}
function _lerpHexColor(c1, c2, t) {
  const [r1, g1, b1] = _hexToRgb(c1), [r2, g2, b2] = _hexToRgb(c2);
  const r = Math.round(r1 + (r2 - r1) * t), g = Math.round(g1 + (g2 - g1) * t), b = Math.round(b1 + (b2 - b1) * t);
  return '#' + [r, g, b].map(x => Math.max(0, Math.min(255, x)).toString(16).padStart(2, '0')).join('');
}

// jfToColor computes the same gradient's color for a plain JS number
// (popups, legends, anywhere outside a GL paint expression) by linearly
// interpolating between the two stops bracketing v.
function jfToColor(v) {
  const stops = jfColorStops();
  if (v === null || v === undefined || Number.isNaN(v)) return stops[0].color;
  if (v <= stops[0].value) return stops[0].color;
  if (v >= stops[stops.length - 1].value) return stops[stops.length - 1].color;
  for (let i = 1; i < stops.length; i++) {
    if (v <= stops[i].value) {
      const a = stops[i - 1], b = stops[i];
      const t = (v - a.value) / (b.value - a.value);
      return _lerpHexColor(a.color, b.color, t);
    }
  }
  return stops[stops.length - 1].color;
}

// Returns the first page the user is permitted to access.
function firstPermittedPage(u) {
  if (!u || u.role === 'admin') return '/map.html';
  const order = [
    { tab: 'map',       page: '/map.html' },
    { tab: 'cameras',   page: '/' },
    { tab: 'dashboard', page: '/dashboard.html' },
    { tab: 'dashboard2', page: '/dashboard2.html' },
    { tab: 'monitor',   page: '/monitor.html' },
    { tab: 'log',       page: '/log.html' },
    { tab: 'config',    page: '/config.html' },
    { tab: 'api_docs',  page: '/api-docs.html' },
    { tab: 'counting',  page: '/counting.html' },
    { tab: 'incidents', page: '/incidents.html' },
  ];
  for (const { tab, page } of order) {
    if ((u.tabs || []).includes(tab)) return page;
  }
  return '/no-access.html';
}

// initChangePasswordModal — wires up "click your name in the sidebar" to
// open a self-service change-password dialog. Injected once (shared across
// every page via app.js, since .user-info/#userName/#userRole/#avatar are
// identical markup on every protected page) rather than duplicated per-page.
function initChangePasswordModal() {
  if (document.getElementById('pwdModalBackdrop')) return; // already wired (e.g. re-entrant initApp call)

  const userInfo = document.querySelector('.user-info');
  if (!userInfo) return;
  userInfo.title = 'Đổi mật khẩu';

  const backdrop = document.createElement('div');
  backdrop.className = 'modal-backdrop';
  backdrop.id = 'pwdModalBackdrop';
  backdrop.innerHTML = `
    <div class="modal" style="max-width:380px">
      <div class="modal-header">
        <h3>Đổi mật khẩu</h3>
        <button class="modal-close" id="pwdModalClose" type="button">✕</button>
      </div>
      <div class="modal-body">
        <div class="form-group">
          <label>Mật khẩu hiện tại</label>
          <input type="password" id="pwdCurrent" class="input" autocomplete="current-password">
        </div>
        <div class="form-group">
          <label>Mật khẩu mới</label>
          <input type="password" id="pwdNew1" class="input" autocomplete="new-password" minlength="8">
          <p style="font-size:.72rem;color:var(--text-muted);margin-top:.3rem">
            Tối thiểu 8 ký tự, gồm ít nhất 1 chữ số và 1 ký tự đặc biệt (ví dụ: ! @ # $ % ...).
          </p>
        </div>
        <div class="form-group">
          <label>Nhập lại mật khẩu mới</label>
          <input type="password" id="pwdNew2" class="input" autocomplete="new-password" minlength="8">
        </div>
        <div id="pwdModalErr" style="color:var(--red);font-size:.8rem;display:none;margin-bottom:.6rem"></div>
        <div style="display:flex;gap:.5rem;justify-content:flex-end">
          <button class="btn btn-secondary" id="pwdModalCancel" type="button">Hủy</button>
          <button class="btn btn-primary" id="pwdModalSave" type="button">Lưu</button>
        </div>
      </div>
    </div>`;
  document.body.appendChild(backdrop);

  const els = id => document.getElementById(id);
  const showErr = msg => { const e = els('pwdModalErr'); e.textContent = msg; e.style.display = ''; };

  const open = () => {
    ['pwdCurrent', 'pwdNew1', 'pwdNew2'].forEach(id => { els(id).value = ''; });
    els('pwdModalErr').style.display = 'none';
    backdrop.classList.add('open');
  };
  const close = () => backdrop.classList.remove('open');

  userInfo.addEventListener('click', open);
  els('pwdModalClose').addEventListener('click', close);
  els('pwdModalCancel').addEventListener('click', close);
  backdrop.addEventListener('click', e => { if (e.target === backdrop) close(); });

  els('pwdModalSave').addEventListener('click', async () => {
    const cur = els('pwdCurrent').value;
    const n1  = els('pwdNew1').value;
    const n2  = els('pwdNew2').value;

    if (!cur || !n1 || !n2) return showErr('Vui lòng nhập đủ các trường.');
    const policyErr = passwordPolicyError(n1);
    if (policyErr) return showErr(policyErr);
    if (n1 !== n2) return showErr('Mật khẩu mới nhập lại không khớp.');
    if (n1 === cur) return showErr('Mật khẩu mới phải khác mật khẩu hiện tại.');

    const btn = els('pwdModalSave');
    btn.disabled = true;
    btn.textContent = 'Đang lưu…';
    try {
      // Plain fetch, not apiFetch: apiFetch treats any 401 as "session
      // expired" and force-redirects to /login.html, but a wrong *current*
      // password here also comes back as 401 — that's not an expired
      // session, just a wrong answer, and should stay on this dialog.
      const res = await fetch('/api/auth/change-password', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', Authorization: 'Bearer ' + getToken() },
        body: JSON.stringify({ current_password: cur, new_password: n1 }),
      });
      if (!res.ok) {
        const txt = await res.text().catch(() => '');
        throw new Error(txt || res.statusText);
      }
      close();
      toast('Đổi mật khẩu thành công.', 'success');
    } catch (e) {
      showErr(e.message || 'Đổi mật khẩu thất bại.');
    } finally {
      btn.disabled = false;
      btn.textContent = 'Lưu';
    }
  });
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

  // A user who must set their own password can't use any other page —
  // the server blocks every other /api/ call for them anyway (Middleware),
  // this just gets them there without a failed-request detour first.
  if (getUser()?.must_change_password && location.pathname !== '/change-password.html') {
    window.location.href = '/change-password.html';
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

  maybeCaptureLoginLocation();

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

  initChangePasswordModal();

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

  // Desktop collapse choice is remembered per browser (same convention as
  // map.html's own 'map_toolbar_collapsed') — previously this reset to
  // expanded on every page load/navigation, which made the toggle feel
  // like it didn't really "stick". Mobile's .open/backdrop overlay is
  // session-only on purpose (always start closed on a fresh page load).
  const SB_COLLAPSED_KEY = 'utmc_sidebar_collapsed';
  if (window.innerWidth > 768 && localStorage.getItem(SB_COLLAPSED_KEY) === '1') {
    document.body.classList.add('sb-collapsed');
  }

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
    document.body.classList.remove('sidebar-open');
    if (window.__map) setTimeout(() => window.__map.invalidateSize(), 260);
  }

  if (toggleBtn && sidebar) {
    toggleBtn.addEventListener('click', () => {
      if (window.innerWidth <= 768) {
        const opening = !sidebar.classList.contains('open');
        sidebar.classList.toggle('open');
        backdrop.classList.toggle('visible', opening);
        document.body.classList.toggle('sidebar-open', opening);
        if (window.__map && !opening) setTimeout(() => window.__map.invalidateSize(), 260);
      } else {
        const collapsed = document.body.classList.toggle('sb-collapsed');
        localStorage.setItem(SB_COLLAPSED_KEY, collapsed ? '1' : '0');
        if (window.__map) setTimeout(() => window.__map.invalidateSize(), 220);
      }
    });
    backdrop.addEventListener('click', closeMobileSidebar);
  }

  // ── Theme toggle (🌙 / ☀️ button in sidebar footer) ─────────────
  const SUN_SVG  = `<svg viewBox="0 0 20 20" fill="currentColor" width="16" height="16"><path fill-rule="evenodd" d="M10 2a1 1 0 011 1v1a1 1 0 11-2 0V3a1 1 0 011-1zm4 8a4 4 0 11-8 0 4 4 0 018 0zm-.464 4.95l.707.707a1 1 0 001.414-1.414l-.707-.707a1 1 0 00-1.414 1.414zm2.12-10.607a1 1 0 010 1.414l-.706.707a1 1 0 11-1.414-1.414l.707-.707a1 1 0 011.414 0zM17 11a1 1 0 100-2h-1a1 1 0 100 2h1zm-7 4a1 1 0 011 1v1a1 1 0 11-2 0v-1a1 1 0 011-1zM5.05 6.464A1 1 0 106.465 5.05l-.708-.707a1 1 0 00-1.414 1.414l.707.707zm1.414 8.486l-.707.707a1 1 0 01-1.414-1.414l.707-.707a1 1 0 011.414 1.414zM4 11a1 1 0 100-2H3a1 1 0 000 2h1z" clip-rule="evenodd"/></svg>`;
  const MOON_SVG = `<svg viewBox="0 0 20 20" fill="currentColor" width="16" height="16"><path d="M17.293 13.293A8 8 0 016.707 2.707a8.001 8.001 0 1010.586 10.586z"/></svg>`;

  // Apply saved preference before render to avoid flash. `data-theme` lives
  // on <html>, not a body class — same mechanism as login.html and the UTMC
  // Platform landing page (docs/utmc-design-system.md), so the one
  // `utmc_theme` localStorage value means the same thing everywhere.
  const savedTheme = localStorage.getItem('utmc_theme');
  if (savedTheme === 'light') document.documentElement.setAttribute('data-theme', 'light');

  const sidebarFooter = document.querySelector('.sidebar-footer');
  if (sidebarFooter) {
    const themeBtn = document.createElement('button');
    themeBtn.id    = 'btnTheme';
    themeBtn.className = 'btn-logout';
    themeBtn.title = 'Toggle light / dark theme';
    const isLight = () => document.documentElement.getAttribute('data-theme') === 'light';
    const updateIcon = () => {
      themeBtn.innerHTML = isLight() ? MOON_SVG : SUN_SVG;
    };
    updateIcon();
    themeBtn.addEventListener('click', () => {
      if (isLight()) document.documentElement.removeAttribute('data-theme');
      else document.documentElement.setAttribute('data-theme', 'light');
      localStorage.setItem('utmc_theme', isLight() ? 'light' : 'dark');
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
