// ai-analyze-hud.js — "AI ANALYZE"/"WATCH 30S" button, tactical HUD overlay
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

// ── Mount: wraps #playerWrap once with a position:relative container so
// the button row / scan overlay / SVG overlay can sit on top of it, and
// appends the stats panel as a normal-flow sibling right after it so it
// pushes the modal taller when shown, matching spec §4.3's "panel expands
// below the image" layout. ────────────────────────────────────────────
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
  btnRow.style.cssText = 'position:absolute;left:8px;bottom:8px;display:flex;gap:6px;z-index:5';
  btnRow.innerHTML =
    `<button id="aiAnalyzeBtn" class="ai-hud-btn" style="border-color:${AI_GOLD};color:${AI_GOLD}">🔍 AI ANALYZE</button>` +
    `<button id="aiWatchBtn" class="ai-hud-btn" style="border-color:${AI_GOLD};color:${AI_GOLD}">⏱ WATCH 30S</button>`;
  relWrap.appendChild(btnRow);

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
  document.getElementById('aiWatchBtn').addEventListener('click', () => startWatch30s());

  watchForPlayerReset();
  return relWrap;
}

function resetAIOverlay() {
  stopWatch();
  const btnRow = document.getElementById('aiBtnRow');
  const scanStatus = document.getElementById('aiScanStatus');
  const svg = document.getElementById('aiSvgOverlay');
  const statsPanel = document.getElementById('aiStatsPanel');
  if (!btnRow) return;
  btnRow.style.display = (typeof canAIAnalyze === 'function' && canAIAnalyze()) ? '' : 'none';
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
// WATCH 30S's first tick) plays the scanning transition; subsequent
// WATCH 30S ticks just re-render in place over the existing overlay —
// replaying the scan animation every 1-5s would be more distracting than
// informative at that cadence. ─────────────────────────────────────────
async function performAnalyze(showScanUI) {
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
  const result = await requestAnalyze(bitmap, (p) => {
    const t = document.getElementById('aiScanText');
    if (t) t.textContent = `READING · PASS ${p.pass}/${p.total} · ${p.found} FOUND`;
  });
  if (showScanUI) scanStatus.style.display = 'none';
  renderAIResult(result);
  return result;
}

let _aiWatchTimer = null;
function stopWatch() {
  if (_aiWatchTimer) { clearInterval(_aiWatchTimer); _aiWatchTimer = null; }
}
async function startWatch30s() {
  stopWatch();
  const isVideo = !!document.querySelector('#playerWrap video-stream');
  const intervalMs = isVideo ? 1000 : 5000; // spec §4.2: 1s for live video, 5s for still images
  const endAt = Date.now() + 30000;
  try { await performAnalyze(true); } catch (e) { showAIError(e); return; }
  _aiWatchTimer = setInterval(async () => {
    if (Date.now() >= endAt) { stopWatch(); return; }
    try { await performAnalyze(false); } catch (_) { /* a single missed tick during WATCH is not worth interrupting the user over */ }
  }, intervalMs);
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
      const utc = new Date().toISOString().slice(11, 19) + 'Z';
      const chipText = d.colour ? `${d.colour.toUpperCase()} ${labelWord(d.label, 1).toUpperCase()}` : d.label.toUpperCase();
      hoverLabel.setAttribute('x', x);
      hoverLabel.setAttribute('y', Math.max(10, y - 6));
      hoverLabel.textContent = `${chipText} · ${pct}% · ${utc}`;
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
  const utc = now.toISOString().slice(11, 19) + 'Z';

  panel.innerHTML = `
    <div style="display:flex;align-items:center;justify-content:space-between;font-size:.66rem;letter-spacing:.1em;color:rgba(255,255,255,.6);text-transform:uppercase">
      <span>👁 ANALYSED ${utc} · ${result.light}</span>
      <button id="aiCloseBtn" class="ai-hud-btn" style="border-color:rgba(255,255,255,.35);color:#fff">✕ LIVE</button>
    </div>
    <div style="display:flex;align-items:flex-end;justify-content:space-between;margin-top:.45rem;flex-wrap:wrap;gap:.5rem">
      <div style="display:flex;gap:1.6rem">
        <div><div id="aiCountVehicles" style="font-size:1.7rem;font-weight:700;color:${AI_CYAN}">0</div><div style="font-size:.6rem;letter-spacing:.15em;color:rgba(255,255,255,.5)">VEHICLES</div></div>
        <div><div id="aiCountPeople" style="font-size:1.7rem;font-weight:700;color:${AI_GOLD}">0</div><div style="font-size:.6rem;letter-spacing:.15em;color:rgba(255,255,255,.5)">PEOPLE</div></div>
      </div>
      <div style="text-align:right;font-size:.66rem;color:rgba(255,255,255,.75)">
        <div>${escHtml(byLabelParts || '—')}</div>
        <div style="color:rgba(255,255,255,.45);margin-top:.15rem">${result.ms} MS · ON THIS DEVICE</div>
      </div>
    </div>
    <div style="display:flex;gap:1px;height:6px;margin-top:.5rem;border-radius:3px;overflow:hidden">${bar}</div>
    <div style="font-size:.6rem;color:rgba(255,255,255,.6);margin-top:.3rem;letter-spacing:.02em">${legend}</div>
    <div style="margin-top:.5rem;padding-top:.4rem;border-top:1px solid rgba(255,255,255,.1);font-size:.58rem;letter-spacing:.04em;color:rgba(255,255,255,.5)"
         title="Read by a detector running in this browser, never collect number plates">
      🔒 Read by a detector running in this browser, never collect number plates
    </div>
  `;
  panel.style.display = '';
  animateCountUp(document.getElementById('aiCountVehicles'), result.counts.vehicles, 1100);
  animateCountUp(document.getElementById('aiCountPeople'), result.counts.people, 1100);
  document.getElementById('aiCloseBtn').addEventListener('click', () => { stopWatch(); resetAIOverlay(); });
}

// ── Boot ─────────────────────────────────────────────────────────────
(function initAIAnalyzeHUD() {
  const start = () => { ensureAIHUDMounted(); resetAIOverlay(); };
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', start);
  else start();
})();
