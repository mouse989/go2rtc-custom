// ai-analyze-hud.js — "AI ANALYZE"/"Watch" button, tactical HUD overlay
// and stats panel for the shared single-camera stream modal (same markup
// on map.html and index.html: #modalBackdrop > .modal-body >
// #modeTabs + #playerWrap). Detection itself runs in ai-analyze.worker.js;
// this file only captures a frame, hands it to the worker, and renders
// the result. Include this script (and ai-analyze.worker.js, served
// alongside it) on any page that has that same modal markup — it wires
// itself up automatically, no page-specific glue required.
//
// 🔒 This detector never reads or stores number plates — it only counts
// vehicles/people and reads each vehicle's exterior colour, entirely in
// this browser. See the disclaimer rendered in the stats panel itself.

const AI_GOLD = '#D4AF37';
const AI_CYAN = '#00E5FF';
const COLOUR_SWATCH = {
  white: '#f4f4f5', silver: '#c4c8cc', grey: '#7c8087', black: '#0d0d0f',
  red: '#d33b3b', orange: '#f08a24', yellow: '#f2c230', green: '#3f9a4a',
  blue: '#2f6fd6', purple: '#8a4fc0', brown: '#7a5236',
};
const LABEL_WORDS = {
  person: { one: 'person', many: 'people' },
  bicycle: { one: 'bicycle', many: 'bicycles' },
  car: { one: 'car', many: 'cars' },
  motorcycle: { one: 'motorbike', many: 'motorbikes' },
  bus: { one: 'bus', many: 'buses' },
  truck: { one: 'truck', many: 'trucks' },
};
function labelWord(label, count) {
  const entry = LABEL_WORDS[label.toLowerCase()];
  if (!entry) return count === 1 ? label : label + 's';
  return count === 1 ? entry.one : entry.many;
}

// This runs entirely in the viewer's browser, so "now" is whatever the
// device clock says — always render it in Vietnam time (ICT, UTC+7, no
// DST) rather than the device's own local zone or raw UTC, since every
// deployment of this system is in Vietnam.
function vnTimeString(date) {
  return date.toLocaleTimeString('en-GB', { timeZone: 'Asia/Ho_Chi_Minh', hour12: false }) + ' ICT';
}

let _aiWorker = null;
let _aiRequestSeq = 0;
const _aiPending = new Map();

function getAIWorker() {
  if (!_aiWorker) {
    _aiWorker = new Worker('/ai-analyze.worker.js');
    _aiWorker.onmessage = (ev) => {
      const msg = ev.data;
      const p = _aiPending.get(msg.requestId);
      if (msg.type === 'progress') { if (p && p.onProgress) p.onProgress(msg); return; }
      if (msg.type === 'result') { _aiPending.delete(msg.requestId); if (p) p.resolve(msg); return; }
      if (msg.type === 'error' && msg.requestId != null) { _aiPending.delete(msg.requestId); if (p) p.reject(new Error(msg.message || 'AI Analyze failed')); return; }
      // type:'ready'/'error' with no requestId — pre-warm response, nothing to do.
    };
  }
  return _aiWorker;
}
// The worker has no access to localStorage (where the auth token lives —
// see app.js's getToken()), so every message to it carries the current
// token; the worker attaches it as a normal Authorization header on its
// own fetches to /api/ai-analyze/*.
function primeAIWorker() { try { getAIWorker().postMessage({ type: 'init', token: getToken() }); } catch (_) {} }

function requestAnalyze(bitmap, onProgress) {
  return new Promise((resolve, reject) => {
    const requestId = ++_aiRequestSeq;
    _aiPending.set(requestId, { resolve, reject, onProgress });
    try {
      getAIWorker().postMessage({ type: 'analyze', requestId, bitmap, enableTiles: true, token: getToken() }, [bitmap]);
    } catch (e) {
      _aiPending.delete(requestId);
      reject(e);
    }
  });
}

// ── Frame capture — reads whatever is currently rendered inside
// #playerWrap, regardless of which mode tab is active. Both snapshot
// (<img class="modal-snap">) and live video (<video-stream>'s own public
// .video element — see video-rtc.js) are already gated behind
// ensureDeviceVerified('cam_snapshot'|'cam_stream') by the time either is
// actually showing something, so this doesn't re-check that itself. ────
async function captureCurrentFrame() {
  const wrap = document.getElementById('playerWrap');
  if (!wrap) throw new Error('no player');
  const img = wrap.querySelector('img.modal-snap');
  if (img && img.complete && img.naturalWidth > 0) {
    return createImageBitmap(img);
  }
  const vs = wrap.querySelector('video-stream');
  if (vs && vs.video && vs.video.readyState >= 2 && vs.video.videoWidth > 0) {
    return createImageBitmap(vs.video);
  }
  throw new Error('Chưa có khung hình để phân tích');
}

// ── One-time styles (sweep animation / scan grid need real @keyframes,
// can't be done with inline style alone) ────────────────────────────────
function ensureAIStyles() {
  if (document.getElementById('aiAnalyzeStyles')) return;
  const style = document.createElement('style');
  style.id = 'aiAnalyzeStyles';
  style.textContent = `
    .ai-hud-btn { background:rgba(0,0,0,.82); border:1px solid; border-radius:4px; padding:.32rem .65rem;
      font-family:ui-monospace,"JetBrains Mono","Share Tech Mono",monospace; font-size:.68rem; letter-spacing:.15em;
      text-transform:uppercase; cursor:pointer; backdrop-filter:blur(6px); }
    .ai-hud-btn:hover { filter:brightness(1.3); }
    .ai-mode-tabs { display:contents; }
    .ai-mode-tab { padding:.28rem .7rem;border-radius:var(--radius);border:1px solid ${AI_GOLD};
      background:rgba(212,175,55,.08);color:${AI_GOLD};font-size:.81rem;font-weight:600;cursor:pointer;
      letter-spacing:.04em;transition:filter .15s,background .15s; }
    .ai-mode-tab:hover { background:rgba(212,175,55,.18); }
    @keyframes ai-sweep { 0% { top:0% } 100% { top:100% } }
    .ai-sweep-line { position:absolute; left:0; right:0; height:2px;
      background:linear-gradient(90deg, transparent, ${AI_GOLD}, transparent); animation: ai-sweep 1.8s linear infinite; }
    .ai-scan-grid { position:absolute; inset:0;
      background-image: linear-gradient(rgba(212,175,55,.14) 1px, transparent 1px), linear-gradient(90deg, rgba(212,175,55,.14) 1px, transparent 1px);
      background-size: 10% 10%; }
    .ai-tactical { font-family:ui-monospace,"JetBrains Mono","Share Tech Mono",monospace; }
  `;
  document.head.appendChild(style);
}

// ── Mount: the AI ANALYZE/Watch buttons are appended as children of
// #modeTabs itself, so they render in the exact same row as
// Snapshot/Auto/WebRTC/MSE/HLS (that container is already a flex row —
// see .mode-tabs in map.html/index.html) rather than a separate row of
// their own. #aiBtnRow wraps them only as a logical/show-hide group
// (display:contents — see .ai-mode-tabs in ensureAIStyles — so it never
// introduces its own box into that flex row); #playerWrap itself gets
// wrapped in a position:relative container so the scanning overlay / SVG
// detection overlay can still sit on top of the image itself. The stats
// panel is appended as a normal-flow sibling right after that, so it
// pushes the modal taller when shown, matching spec §4.3's "panel expands
// below the image" layout. ──────────────────────────────────────────────
function ensureAIHUDMounted() {
  const playerWrap = document.getElementById('playerWrap');
  if (!playerWrap) return null;
  if (playerWrap.dataset.aiMounted) return document.getElementById('aiAnalyzeRelWrap');
  playerWrap.dataset.aiMounted = '1';
  ensureAIStyles();

  const relWrap = document.createElement('div');
  relWrap.id = 'aiAnalyzeRelWrap';
  relWrap.style.position = 'relative';
  playerWrap.parentElement.insertBefore(relWrap, playerWrap);
  relWrap.appendChild(playerWrap);

  const btnRow = document.createElement('div');
  btnRow.id = 'aiBtnRow';
  btnRow.className = 'ai-mode-tabs';
  btnRow.innerHTML =
    `<button id="aiAnalyzeBtn" class="ai-mode-tab">🔍 AI ANALYZE</button>` +
    `<button id="aiWatchBtn" class="ai-mode-tab">⏱ Watch</button>`;
  // Appended as a CHILD of #modeTabs (not a sibling) so it visually joins
  // that same row — safe because these buttons use the distinct
  // "ai-mode-tab" class, never the page's own "mode-tab" class, so
  // #modeTabs' delegated click handler (which does
  // e.target.closest('.mode-tab') and calls startPlayer(...) on a match)
  // never matches a click on either of them, however deep they sit inside
  // #modeTabs. Their own click listeners below fire independently.
  const modeTabs = document.getElementById('modeTabs');
  if (modeTabs) {
    modeTabs.appendChild(btnRow);
  } else {
    relWrap.parentElement.insertBefore(btnRow, relWrap);
  }

  // Hidden until WATCH freezes a frame on it (see freezeDisplayedFrame())
  // — sits above the live video but below the scan overlay/SVG, same
  // aspect-ratio-preserving width:100%/height:100% technique the SVG
  // overlay already uses via its viewBox (this container is sized FROM
  // the video's own intrinsic aspect ratio, not the other way around, so
  // no object-fit/letterboxing math is needed here either).
  const freezeCanvas = document.createElement('canvas');
  freezeCanvas.id = 'aiFreezeCanvas';
  freezeCanvas.style.cssText = 'position:absolute;inset:0;width:100%;height:100%;display:none;z-index:3;pointer-events:none';
  relWrap.appendChild(freezeCanvas);

  const scanStatus = document.createElement('div');
  scanStatus.id = 'aiScanStatus';
  scanStatus.style.cssText = 'position:absolute;inset:0;display:none;z-index:6;pointer-events:none';
  relWrap.appendChild(scanStatus);

  const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
  svg.id = 'aiSvgOverlay';
  svg.setAttribute('style', 'position:absolute;inset:0;width:100%;height:100%;display:none;z-index:7');
  relWrap.appendChild(svg);

  const statsPanel = document.createElement('div');
  statsPanel.id = 'aiStatsPanel';
  statsPanel.className = 'ai-tactical';
  statsPanel.style.cssText = 'display:none;background:#000;border-top:1px solid rgba(255,255,255,.1);padding:.6rem .8rem;color:#fff';
  relWrap.parentElement.insertBefore(statsPanel, relWrap.nextSibling);

  document.getElementById('aiAnalyzeBtn').addEventListener('pointerenter', primeAIWorker);
  document.getElementById('aiAnalyzeBtn').addEventListener('click', () => performAnalyze(true).catch(showAIError));
  document.getElementById('aiWatchBtn').addEventListener('pointerenter', primeAIWorker);
  document.getElementById('aiWatchBtn').addEventListener('click', () => startWatch());

  watchForPlayerReset();
  return relWrap;
}

// ── Frame freeze (WATCH on live video only — see freezeDisplayedFrame()
// for why) ───────────────────────────────────────────────────────────
let _aiFreezeStagingCanvas = null; // off-DOM scratch buffer, never shown directly
function stageFrameForFreeze(bitmap) {
  // Reads the bitmap via drawImage without consuming it — the caller is
  // free to still transfer the same bitmap to the worker afterward.
  if (!_aiFreezeStagingCanvas) _aiFreezeStagingCanvas = document.createElement('canvas');
  _aiFreezeStagingCanvas.width = bitmap.width;
  _aiFreezeStagingCanvas.height = bitmap.height;
  _aiFreezeStagingCanvas.getContext('2d').drawImage(bitmap, 0, 0);
}
// Swaps the staged frame onto the VISIBLE freeze canvas and shows it —
// call this and renderAIResult() back-to-back (same synchronous tick, no
// await between them) so the displayed frame and its detection boxes
// always change together, never one without the other.
function freezeDisplayedFrame() {
  const canvas = document.getElementById('aiFreezeCanvas');
  if (!canvas || !_aiFreezeStagingCanvas) return;
  canvas.width = _aiFreezeStagingCanvas.width;
  canvas.height = _aiFreezeStagingCanvas.height;
  canvas.getContext('2d').drawImage(_aiFreezeStagingCanvas, 0, 0);
  canvas.style.display = '';
}
function unfreezeDisplayedFrame() {
  const canvas = document.getElementById('aiFreezeCanvas');
  if (canvas) canvas.style.display = 'none';
}

function resetAIOverlay() {
  stopWatch();
  const btnRow = document.getElementById('aiBtnRow');
  const analyzeBtn = document.getElementById('aiAnalyzeBtn');
  const watchBtn = document.getElementById('aiWatchBtn');
  const scanStatus = document.getElementById('aiScanStatus');
  const svg = document.getElementById('aiSvgOverlay');
  const statsPanel = document.getElementById('aiStatsPanel');
  if (!btnRow) return;
  unfreezeDisplayedFrame();
  // AllowAIAnalyze and AllowAIWatch are independent permissions — each
  // button shows/hides on its own flag, not as a single all-or-nothing row
  // (btnRow itself is never hidden here; it's display:contents and only
  // ever forced to 'none' transiently by performAnalyze()/showAIError()
  // below, during an active scan).
  analyzeBtn.style.display = (typeof canAIAnalyze === 'function' && canAIAnalyze()) ? '' : 'none';
  watchBtn.style.display = (typeof canAIWatch === 'function' && canAIWatch()) ? '' : 'none';
  btnRow.style.display = '';
  scanStatus.style.display = 'none';
  scanStatus.innerHTML = '';
  svg.style.display = 'none';
  svg.innerHTML = '';
  statsPanel.style.display = 'none';
  statsPanel.innerHTML = '';
}

// Every mode switch (snapshot⇄webrtc⇄mse⇄hls) AND every new camera open
// both clear #playerWrap's children first (see map.html/index.html's own
// showSnapshot/startPlayer) — watching for that, plus the modal's own
// open/close via its backdrop class, means this file never has to know
// either page's own function names to stay in sync with them.
function watchForPlayerReset() {
  const playerWrap = document.getElementById('playerWrap');
  if (playerWrap) {
    new MutationObserver(resetAIOverlay).observe(playerWrap, { childList: true });
  }
  const backdrop = document.getElementById('modalBackdrop');
  if (backdrop) {
    new MutationObserver(() => {
      if (!backdrop.classList.contains('open')) resetAIOverlay();
    }).observe(backdrop, { attributes: true, attributeFilter: ['class'] });
  }
}

function showAIError(e) {
  const scanStatus = document.getElementById('aiScanStatus');
  const btnRow = document.getElementById('aiBtnRow');
  if (!scanStatus) return;
  scanStatus.style.display = '';
  scanStatus.innerHTML = `<div style="position:absolute;left:8px;bottom:8px;color:#f87171;font-family:ui-monospace,monospace;font-size:.7rem;background:rgba(0,0,0,.82);padding:.25rem .55rem;border-radius:3px">⚠ ${escHtml(e && e.message ? e.message : String(e))}</div>`;
  setTimeout(() => { if (scanStatus) scanStatus.style.display = 'none'; if (btnRow) btnRow.style.display = ''; }, 2600);
}

// ── Run one analysis pass. showScanUI=true (the AI ANALYZE click, and
// WATCH's first tick) plays the scanning transition; subsequent WATCH
// ticks just re-render in place over the existing overlay — replaying the
// scan animation every 1-5s would be more distracting than informative at
// that cadence.
//
// freezeFrame=true (WATCH on a live video source only — see
// startWatch()) stages the captured frame AND swaps it onto the
// visible freeze canvas in the same synchronous pass that renders the
// boxes, so what's on screen and what the boxes describe are always the
// exact same instant. Without this, the live <video> keeps playing under
// the overlay at its own 12-20fps while WATCH can only analyze a fraction
// of that — the boxes would visibly lag behind a moving vehicle. Freezing
// trades that for a slower, "dropped fps" slideshow of (frame, result)
// pairs that are always correct together, paced at whatever rate
// startWatch()'s self-pacing loop already settled on for this device.
async function performAnalyze(showScanUI, freezeFrame) {
  const btnRow = document.getElementById('aiBtnRow');
  const scanStatus = document.getElementById('aiScanStatus');
  if (showScanUI) {
    btnRow.style.display = 'none';
    scanStatus.style.display = '';
    scanStatus.innerHTML =
      '<div class="ai-scan-grid"></div><div class="ai-sweep-line"></div>' +
      `<div id="aiScanText" style="position:absolute;left:8px;bottom:8px;font-family:ui-monospace,monospace;font-size:.68rem;letter-spacing:.1em;color:${AI_GOLD};background:rgba(0,0,0,.78);padding:.22rem .55rem;border-radius:3px">LOADING DETECTOR</div>`;
  }
  const bitmap = await captureCurrentFrame();
  if (freezeFrame) stageFrameForFreeze(bitmap); // reads the bitmap; doesn't consume it — still safe to transfer below
  const result = await requestAnalyze(bitmap, (p) => {
    const t = document.getElementById('aiScanText');
    if (t) t.textContent = `READING · PASS ${p.pass}/${p.total} · ${p.found} FOUND`;
  });
  if (showScanUI) scanStatus.style.display = 'none';
  if (freezeFrame) freezeDisplayedFrame(); // same synchronous pass as renderAIResult() below — frame and boxes swap together
  renderAIResult(result);
  return result;
}

let _aiWatchTimer = null;
let _aiWatchToken = null;
function stopWatch() {
  if (_aiWatchTimer) { clearTimeout(_aiWatchTimer); _aiWatchTimer = null; }
  _aiWatchToken = null; // invalidates any tick currently awaiting performAnalyze()
  unfreezeDisplayedFrame(); // restore the live video view
}

// ── WATCH's loop is self-pacing, not a fixed setInterval: each tick only
// schedules the NEXT one after the current performAnalyze() has fully
// resolved and rendered, with the gap = max(target cadence, actual time
// that pass took on THIS device). A fixed-interval timer would instead
// fire the next tick even while a slow device is still working through
// the previous one — on a weak CPU that piles up overlapping requests on
// the single ONNX worker (it has no choice but to queue them, since
// there's one WASM session), each working from an increasingly stale
// captured frame, and memory grows with every bitmap still waiting to be
// processed. Pacing off the measured elapsed time instead means a slow
// browser naturally settles into whatever rate it can actually sustain —
// skipping the video frames in between rather than falling behind them —
// while a fast one still never exceeds the spec's target cadence (1s for
// live video, 5s for still images), since we take the max.
//
// Runs indefinitely — not capped at 30s — until stopWatch() is called,
// which happens automatically via resetAIOverlay() the moment the user
// switches stream mode, opens a different camera, or closes the modal
// (see watchForPlayerReset()). ──────────────────────────────────────────
async function startWatch() {
  stopWatch();
  const isVideo = !!document.querySelector('#playerWrap video-stream');
  const targetMs = isVideo ? 1000 : 5000; // spec §4.2: 1s for live video, 5s for still images
  const token = {};
  _aiWatchToken = token;

  // One loop for the first (scan-UI) pass and every periodic tick after
  // it, so the gap after the FIRST pass is just as adaptive as every
  // later one — a slow initial pass must not still be followed by a flat
  // targetMs wait on top of it.
  const tick = async (showScanUI) => {
    if (_aiWatchToken !== token) return; // stopWatch() ran while this tick was scheduled/pending
    const t0 = performance.now();
    try {
      // Freeze the displayed frame only for live video — a snapshot
      // source is already a single static image between its own
      // auto-refreshes, so there's no live motion to desync from.
      await performAnalyze(showScanUI, isVideo);
    } catch (e) {
      // First pass failing aborts WATCH entirely; a single missed periodic
      // tick is not worth interrupting the user over — fall through below
      // to reschedule the next one as usual.
      if (showScanUI) { showAIError(e); return; }
    }
    if (_aiWatchToken !== token) return; // stopWatch() ran while the analyze above was in flight
    const elapsed = performance.now() - t0;
    _aiWatchTimer = setTimeout(() => tick(false), Math.max(0, targetMs - elapsed));
  };
  await tick(true);
}

// ── Rendering ────────────────────────────────────────────────────────
function cornerBracketsSVG(x, y, w, h, color) {
  const L = Math.max(4, Math.min(14, w * 0.25, h * 0.25));
  return (
    `<path d="M${x},${y + L} L${x},${y} L${x + L},${y}" stroke="${color}" stroke-width="2" fill="none"/>` +
    `<path d="M${x + w - L},${y} L${x + w},${y} L${x + w},${y + L}" stroke="${color}" stroke-width="2" fill="none"/>` +
    `<path d="M${x + w},${y + h - L} L${x + w},${y + h} L${x + w - L},${y + h}" stroke="${color}" stroke-width="2" fill="none"/>` +
    `<path d="M${x + L},${y + h} L${x},${y + h} L${x},${y + h - L}" stroke="${color}" stroke-width="2" fill="none"/>`
  );
}

function placeLabels(dets, topIdx) {
  // Greedy anti-overlap: default position is just above the box's top
  // edge; if that collides with an already-placed label's rect, nudge it
  // down in steps until clear. Not a specific published algorithm — this
  // implementation's own approach to the spec's "labels never overlap"
  // requirement.
  const placed = [];
  const out = new Map();
  for (const i of topIdx) {
    const [x, y, w] = dets[i].box;
    const labelW = Math.max(60, Math.min(140, w));
    const labelH = 14;
    let ly = y - labelH - 2;
    for (let guard = 0; guard < 20; guard++) {
      const rect = { x, y: ly, w: labelW, h: labelH };
      const collide = placed.some(p => !(rect.x + rect.w < p.x || p.x + p.w < rect.x || rect.y + rect.h < p.y || p.y + p.h < rect.y));
      if (!collide) break;
      ly += labelH + 2;
    }
    placed.push({ x, y: ly, w: labelW, h: labelH });
    out.set(i, { x, y: ly });
  }
  return out;
}

function renderAIResult(result) {
  const svg = document.getElementById('aiSvgOverlay');
  const wrap = document.getElementById('playerWrap');
  if (!svg || !wrap) return;
  svg.setAttribute('viewBox', `0 0 ${result.width} ${result.height}`);
  svg.style.display = '';

  const vehicleSet = new Set((result.vehicleLabels || []).map(l => l.toLowerCase()));
  const dets = result.detections;

  // Default labels on up to the 6 largest vehicles by box area.
  const vehicleIdx = dets.map((d, i) => i).filter(i => vehicleSet.has(dets[i].label.toLowerCase()));
  vehicleIdx.sort((a, b) => (dets[b].box[2] * dets[b].box[3]) - (dets[a].box[2] * dets[a].box[3]));
  const topIdx = new Set(vehicleIdx.slice(0, 6));
  const labelPos = placeLabels(dets, [...topIdx]);

  let html = '';
  dets.forEach((d, i) => {
    const [x, y, w, h] = d.box;
    const isVehicle = vehicleSet.has(d.label.toLowerCase());
    const color = isVehicle ? AI_CYAN : AI_GOLD;
    const swatch = d.colour ? COLOUR_SWATCH[d.colour] : null;
    const chipText = d.colour ? `${d.colour.toUpperCase()} ${labelWord(d.label, 1).toUpperCase()}` : d.label.toUpperCase();

    html += `<g class="ai-det" data-idx="${i}" style="cursor:pointer">`;
    html += `<rect x="${x}" y="${y}" width="${w}" height="${h}" fill="none" stroke="${color}" stroke-opacity="0.3" stroke-width="1"/>`;
    html += cornerBracketsSVG(x, y, w, h, color);
    if (swatch) {
      const sw = Math.min(14, w * 0.2);
      html += `<rect x="${x + w - sw - 3}" y="${y + 3}" width="${sw}" height="${sw}" fill="${swatch}" stroke="#fff" stroke-width="1" stroke-opacity="0.7"/>`;
    }
    if (labelPos.has(i)) {
      const p = labelPos.get(i);
      html += `<text x="${p.x}" y="${p.y + 10}" font-size="10" font-family="ui-monospace,monospace" fill="${color}" style="paint-order:stroke" stroke="#000" stroke-width="2.5">${escHtml(chipText)}</text>`;
    }
    html += `</g>`;
  });
  svg.innerHTML = html;

  svg.querySelectorAll('.ai-det').forEach(g => {
    const i = +g.dataset.idx;
    const d = dets[i];
    g.addEventListener('pointerenter', () => {
      svg.querySelectorAll('.ai-det').forEach(o => o.style.opacity = o === g ? '1' : '.22');
      g.querySelectorAll('rect,path').forEach(el => el.setAttribute('stroke-width', el.tagName === 'path' ? '2.5' : el.getAttribute('stroke-width')));
      let hoverLabel = svg.querySelector('#aiHoverLabel');
      if (!hoverLabel) {
        hoverLabel = document.createElementNS('http://www.w3.org/2000/svg', 'text');
        hoverLabel.id = 'aiHoverLabel';
        hoverLabel.setAttribute('font-size', '10');
        hoverLabel.setAttribute('font-family', 'ui-monospace,monospace');
        hoverLabel.setAttribute('fill', '#fff');
        hoverLabel.setAttribute('stroke', '#000');
        hoverLabel.setAttribute('stroke-width', '2.5');
        hoverLabel.setAttribute('style', 'paint-order:stroke');
        svg.appendChild(hoverLabel);
      }
      const [x, y] = d.box;
      const pct = Math.round(d.score * 100);
      const vnTime = vnTimeString(new Date());
      const chipText = d.colour ? `${d.colour.toUpperCase()} ${labelWord(d.label, 1).toUpperCase()}` : d.label.toUpperCase();
      hoverLabel.setAttribute('x', x);
      hoverLabel.setAttribute('y', Math.max(10, y - 6));
      hoverLabel.textContent = `${chipText} · ${pct}% · ${vnTime}`;
      hoverLabel.style.display = '';
    });
    g.addEventListener('pointerleave', () => {
      svg.querySelectorAll('.ai-det').forEach(o => o.style.opacity = '1');
      const hoverLabel = svg.querySelector('#aiHoverLabel');
      if (hoverLabel) hoverLabel.style.display = 'none';
    });
  });

  renderAIStatsPanel(result);
}

function animateCountUp(el, target, durationMs) {
  if (!el) return;
  const start = performance.now();
  function tick(now) {
    const t = Math.min(1, (now - start) / durationMs);
    el.textContent = Math.round(target * (1 - Math.pow(1 - t, 3))); // ease-out cubic
    if (t < 1) requestAnimationFrame(tick);
  }
  requestAnimationFrame(tick);
}

function renderAIStatsPanel(result) {
  const panel = document.getElementById('aiStatsPanel');
  if (!panel) return;
  const vehicleSet = new Set((result.vehicleLabels || []).map(l => l.toLowerCase()));
  const byLabelParts = Object.entries(result.counts.byLabel)
    .filter(([l]) => vehicleSet.has(l.toLowerCase()))
    .sort((a, b) => b[1] - a[1])
    .map(([l, n]) => `${n} ${labelWord(l, n).toUpperCase()}`)
    .join(' · ');

  const colourEntries = Object.entries(result.coloursCount).sort((a, b) => b[1] - a[1]);
  const bar = colourEntries.length
    ? colourEntries.map(([c, n]) => `<div style="flex-grow:${n};background:${COLOUR_SWATCH[c] || '#888'}"></div>`).join('')
    : '<div style="flex-grow:1;background:rgba(255,255,255,.12)"></div>';
  const legend = colourEntries.map(([c, n]) => `<span style="color:${COLOUR_SWATCH[c] || '#888'}">■</span> ${n} ${c.toUpperCase()}`).join('  ');

  const now = new Date();
  const vnTime = vnTimeString(now);

  panel.innerHTML = `
    <div style="display:flex;align-items:center;justify-content:space-between;flex-wrap:wrap;gap:.25rem .6rem;font-size:.66rem;letter-spacing:.1em;color:rgba(255,255,255,.6);text-transform:uppercase">
      <span>👁 ANALYSED ${vnTime} · ${result.light}
        <span style="text-transform:none;letter-spacing:.02em;color:rgba(255,255,255,.4);margin-left:.6rem"
              title="Read by a detector running in this browser, never collect number plates">
          🔒 Read by a detector running in this browser, never collect number plates
        </span>
      </span>
      <button id="aiCloseBtn" class="ai-hud-btn" style="border-color:rgba(255,255,255,.35);color:#fff">✕ LIVE</button>
    </div>
    <div style="display:flex;align-items:flex-end;justify-content:space-between;margin-top:.45rem;flex-wrap:wrap;gap:.5rem">
      <div style="display:flex;gap:1.6rem">
        <div><div id="aiCountVehicles" style="font-size:1.7rem;font-weight:700;color:${AI_CYAN}">0</div><div style="font-size:.6rem;letter-spacing:.15em;color:rgba(255,255,255,.5)">VEHICLES</div></div>
      </div>
      <div style="text-align:right;font-size:.66rem;color:rgba(255,255,255,.75)">
        <div>${escHtml(byLabelParts || '—')}</div>
        <div style="color:rgba(255,255,255,.45);margin-top:.15rem">${result.ms} MS · ON THIS DEVICE</div>
      </div>
    </div>
    <div style="display:flex;gap:1px;height:6px;margin-top:.5rem;border-radius:3px;overflow:hidden">${bar}</div>
    <div style="font-size:.6rem;color:rgba(255,255,255,.6);margin-top:.3rem;letter-spacing:.02em">${legend}</div>
  `;
  panel.style.display = '';
  animateCountUp(document.getElementById('aiCountVehicles'), result.counts.vehicles, 1100);
  document.getElementById('aiCloseBtn').addEventListener('click', () => { stopWatch(); resetAIOverlay(); });
}

// ── Boot ─────────────────────────────────────────────────────────────
(function initAIAnalyzeHUD() {
  const start = () => { ensureAIHUDMounted(); resetAIOverlay(); };
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', start);
  else start();
})();
