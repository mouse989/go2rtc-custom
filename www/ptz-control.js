// ptz-control.js — Pan/Tilt/Zoom overlay for the live-video modal, shared by
// index.html and map.html.
//
// Call mountPTZOverlay(container, streamName) ONLY when the stream's
// /api/proxy/streams entry already reported ptz:true — that flag already
// combines the viewer's AllowPTZ permission with the camera's own PTZ
// configuration (see internal/auth/ptz.go). This module never makes that
// decision itself and never renders anything on its own initiative; the
// host page is responsible for not calling it otherwise, so a viewer
// without permission never has this overlay appear in the page at all.
//
// Returns an unmount() function the host page MUST call, before clearing
// or replacing `container`, so a direction/zoom button held at the moment
// the player is torn down (mode switch, modal close) still gets its
// Stop sent — otherwise a camera using the Axis VAPIX driver (which has no
// built-in move timeout, unlike the ONVIF driver's Timeout param) would
// keep moving indefinitely.

function mountPTZOverlay(container, streamName) {
  const DIRS = [
    { id: 'n', dx: 0, dy: -50, rot: 0, pan: 0, tilt: 1 },
    { id: 'ne', dx: 35, dy: -35, rot: 45, pan: 1, tilt: 1 },
    { id: 'e', dx: 50, dy: 0, rot: 90, pan: 1, tilt: 0 },
    { id: 'se', dx: 35, dy: 35, rot: 135, pan: 1, tilt: -1 },
    { id: 's', dx: 0, dy: 50, rot: 180, pan: 0, tilt: -1 },
    { id: 'sw', dx: -35, dy: 35, rot: 225, pan: -1, tilt: -1 },
    { id: 'w', dx: -50, dy: 0, rot: 270, pan: -1, tilt: 0 },
    { id: 'nw', dx: -35, dy: -35, rot: 315, pan: -1, tilt: 1 },
  ];
  const IDLE_BG = 'rgba(255,255,255,.08)';
  const IDLE_BORDER = 'rgba(255,255,255,.22)';
  const ACTIVE_BG = '#2f81f7';

  let pressed = false;

  async function sendMove(pan, tilt, zoom) {
    try {
      await apiFetch('/api/ptz/move', { method: 'POST', body: { stream: streamName, pan, tilt, zoom } });
    } catch (e) {
      console.error('[ptz] move failed', e);
    }
  }
  async function sendStop() {
    try {
      await apiFetch('/api/ptz/stop', { method: 'POST', body: { stream: streamName } });
    } catch (e) {
      console.error('[ptz] stop failed', e);
    }
  }
  async function sendHome() {
    try {
      await apiFetch('/api/ptz/home', { method: 'POST', body: { stream: streamName } });
    } catch (e) {
      console.error('[ptz] home failed', e);
    }
  }

  const wrap = document.createElement('div');
  wrap.className = 'ptz-overlay';
  wrap.style.cssText = 'position:absolute;right:16px;bottom:16px;display:flex;align-items:center;gap:12px;z-index:5';

  // ── Pan/Tilt joystick (8-direction), with a Home button at its center ──
  const pad = document.createElement('div');
  pad.style.cssText = 'position:relative;width:120px;height:120px;border-radius:50%;background:rgba(13,17,23,.6);backdrop-filter:blur(6px);border:1px solid rgba(255,255,255,.14);flex-shrink:0';

  // Recalls preset 1 (the camera's home position, set up ahead of time on
  // the camera itself) — a single click, not hold-to-move like the
  // direction/zoom buttons, so it gets its own simple busy-guarded click
  // handler instead of wireHoldButton.
  const homeBtn = document.createElement('button');
  homeBtn.type = 'button';
  homeBtn.setAttribute('aria-label', 'Về vị trí home (preset 1)');
  homeBtn.style.cssText = `position:absolute;left:50%;top:50%;width:40px;height:40px;margin-left:-20px;margin-top:-20px;` +
    `border-radius:50%;border:1px solid ${IDLE_BORDER};background:${IDLE_BG};color:#e6edf3;` +
    `display:flex;align-items:center;justify-content:center;cursor:pointer;padding:0`;
  homeBtn.innerHTML = '<svg width="16" height="16" viewBox="0 0 24 24" fill="none">' +
    '<path d="M3 11.5 12 4l9 7.5" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/>' +
    '<path d="M5.5 10.5V20a1 1 0 0 0 1 1H9a1 1 0 0 0 1-1v-4a1 1 0 0 1 1-1h2a1 1 0 0 1 1 1v4a1 1 0 0 0 1 1h2.5a1 1 0 0 0 1-1v-9.5" ' +
    'stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/></svg>';
  let homeBusy = false;
  homeBtn.addEventListener('click', async () => {
    if (homeBusy) return;
    homeBusy = true;
    homeBtn.style.background = ACTIVE_BG;
    homeBtn.style.borderColor = ACTIVE_BG;
    await sendHome();
    homeBusy = false;
    homeBtn.style.background = IDLE_BG;
    homeBtn.style.borderColor = IDLE_BORDER;
  });
  pad.appendChild(homeBtn);

  function wireHoldButton(btn, onDown) {
    const down = (e) => {
      e.preventDefault();
      pressed = true;
      btn.style.background = ACTIVE_BG;
      btn.style.borderColor = ACTIVE_BG;
      onDown();
    };
    const up = () => {
      if (!pressed) return;
      pressed = false;
      btn.style.background = IDLE_BG;
      btn.style.borderColor = IDLE_BORDER;
      sendStop();
    };
    btn.addEventListener('mousedown', down);
    btn.addEventListener('mouseup', up);
    btn.addEventListener('mouseleave', up);
    btn.addEventListener('touchstart', down, { passive: false });
    btn.addEventListener('touchend', up);
  }

  DIRS.forEach((d) => {
    const btn = document.createElement('button');
    btn.type = 'button';
    btn.setAttribute('aria-label', 'Hướng ' + d.id);
    btn.style.cssText = `position:absolute;left:50%;top:50%;width:30px;height:30px;margin-left:-15px;margin-top:-15px;` +
      `transform:translate(${d.dx}px,${d.dy}px);border-radius:50%;border:1px solid ${IDLE_BORDER};background:${IDLE_BG};` +
      `color:#e6edf3;display:flex;align-items:center;justify-content:center;cursor:pointer;padding:0`;
    btn.innerHTML = `<svg width="11" height="11" viewBox="0 0 24 24" fill="none" style="transform:rotate(${d.rot}deg)">` +
      `<path d="M12 4 L19 16 L12 12.5 L5 16 Z" fill="currentColor"></path></svg>`;
    wireHoldButton(btn, () => sendMove(d.pan, d.tilt, 0));
    pad.appendChild(btn);
  });

  // ── Zoom +/- ──────────────────────────────────────────────────────
  const zoomPill = document.createElement('div');
  zoomPill.style.cssText = 'width:34px;height:76px;border-radius:17px;background:rgba(13,17,23,.6);backdrop-filter:blur(6px);' +
    'border:1px solid rgba(255,255,255,.14);display:flex;flex-direction:column;overflow:hidden;flex-shrink:0';

  [{ label: '+', zoom: 1, aria: 'Zoom vào' }, { label: '−', zoom: -1, aria: 'Zoom ra' }].forEach((z, i) => {
    const btn = document.createElement('button');
    btn.type = 'button';
    btn.setAttribute('aria-label', z.aria);
    btn.textContent = z.label;
    btn.style.cssText = 'flex:1;border:none;background:transparent;color:#e6edf3;font-size:16px;font-weight:600;' +
      'display:flex;align-items:center;justify-content:center;cursor:pointer;padding:0';
    wireHoldButton(btn, () => sendMove(0, 0, z.zoom));
    zoomPill.appendChild(btn);
    if (i === 0) {
      const divider = document.createElement('div');
      divider.style.cssText = 'height:1px;background:rgba(255,255,255,.14)';
      zoomPill.appendChild(divider);
    }
  });

  wrap.appendChild(zoomPill);
  wrap.appendChild(pad);
  if (!container.style.position) container.style.position = 'relative';
  container.appendChild(wrap);

  return function unmount() {
    if (pressed) {
      pressed = false;
      sendStop();
    }
  };
}
