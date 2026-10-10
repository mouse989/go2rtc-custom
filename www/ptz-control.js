// ptz-control.js — Pan/Tilt/Zoom/Focus overlay for the live-video modal,
// shared by index.html and map.html.
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
// or replacing `container`, so a direction/zoom/focus button held at the
// moment the player is torn down (mode switch, modal close) still gets its
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

  // The one hold-button that's currently down, as its own stop() callback —
  // not a boolean, because unmount() needs to call the *matching* stop
  // (sendStop for pan/tilt/zoom, sendFocusStop for focus — see
  // wireHoldButton) rather than always assuming pan/tilt/zoom.
  let activeStopFn = null;

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
  async function sendFocus(speed) {
    try {
      await apiFetch('/api/ptz/focus', { method: 'POST', body: { stream: streamName, speed } });
    } catch (e) {
      console.error('[ptz] focus failed', e);
    }
  }
  async function sendFocusStop() {
    try {
      await apiFetch('/api/ptz/focus-stop', { method: 'POST', body: { stream: streamName } });
    } catch (e) {
      console.error('[ptz] focus-stop failed', e);
    }
  }
  async function sendHome() {
    try {
      await apiFetch('/api/ptz/home', { method: 'POST', body: { stream: streamName } });
    } catch (e) {
      console.error('[ptz] home failed', e);
    }
  }
  async function sendSaveHome() {
    try {
      await apiFetch('/api/ptz/save-home', { method: 'POST', body: { stream: streamName } });
      return true;
    } catch (e) {
      console.error('[ptz] save-home failed', e);
      return false;
    }
  }

  // ── Show/hide toggle ─────────────────────────────────────────────
  // The full cluster (joystick + 2 side pills) is ~210px wide — fine over a
  // desktop-size video, but on a phone/tablet modal (often well under
  // 400px wide, see the live-video modal on map.html/index.html) it covers
  // a large share of the frame. Collapsed by default below that width;
  // always shown above it, preserving the previous always-visible
  // behaviour on desktop. Once the viewer taps the toggle, their choice is
  // remembered (per browser) and wins over that width guess from then on.
  const VISIBLE_KEY = 'go2rtc_ptz_overlay_visible';
  const NARROW_WIDTH = 480;
  const savedVisible = localStorage.getItem(VISIBLE_KEY);
  let visible = savedVisible !== null ? savedVisible === '1' : window.innerWidth > NARROW_WIDTH;

  const toggleBtn = document.createElement('button');
  toggleBtn.type = 'button';
  toggleBtn.setAttribute('aria-label', 'Hiện/ẩn điều khiển PTZ');
  toggleBtn.title = 'Hiện/ẩn điều khiển PTZ';
  toggleBtn.style.cssText = `position:absolute;left:10px;bottom:10px;width:34px;height:34px;border-radius:50%;` +
    `border:1px solid ${IDLE_BORDER};background:${IDLE_BG};backdrop-filter:blur(6px);color:#e6edf3;` +
    `display:flex;align-items:center;justify-content:center;cursor:pointer;padding:0;z-index:6`;
  toggleBtn.innerHTML = '<svg width="16" height="16" viewBox="0 0 24 24" fill="none">' +
    '<circle cx="12" cy="12" r="3" fill="currentColor"/>' +
    '<path d="M12 2 L15 7 L9 7 Z" fill="currentColor"/><path d="M12 22 L15 17 L9 17 Z" fill="currentColor"/>' +
    '<path d="M2 12 L7 9 L7 15 Z" fill="currentColor"/><path d="M22 12 L17 9 L17 15 Z" fill="currentColor"/></svg>';

  const wrap = document.createElement('div');
  wrap.className = 'ptz-overlay';
  wrap.style.cssText = 'position:absolute;right:16px;bottom:16px;align-items:center;gap:12px;z-index:5';

  function applyVisibility() {
    wrap.style.display = visible ? 'flex' : 'none';
    toggleBtn.style.background = visible ? ACTIVE_BG : IDLE_BG;
    toggleBtn.style.borderColor = visible ? ACTIVE_BG : IDLE_BORDER;
  }
  toggleBtn.addEventListener('click', () => {
    visible = !visible;
    localStorage.setItem(VISIBLE_KEY, visible ? '1' : '0');
    applyVisibility();
  });

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

  // stopFn defaults to sendStop (pan/tilt/zoom) — focus buttons pass
  // sendFocusStop instead, since releasing a focus button must never issue
  // a pan/tilt/zoom Stop (and vice versa): the two are genuinely separate
  // operations on both drivers (see ptz.go's SendPTZFocus doc comment).
  function wireHoldButton(btn, onDown, stopFn = sendStop) {
    const down = (e) => {
      e.preventDefault();
      activeStopFn = stopFn;
      btn.style.background = ACTIVE_BG;
      btn.style.borderColor = ACTIVE_BG;
      onDown();
    };
    const up = () => {
      if (activeStopFn !== stopFn) return; // already released
      activeStopFn = null;
      btn.style.background = IDLE_BG;
      btn.style.borderColor = IDLE_BORDER;
      stopFn();
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

  // ── Focus near/far ───────────────────────────────────────────────
  // Icons follow the usual convention on camera control UIs: mountains
  // (a wide/panoramic scene) for Far, a flower (a macro/close-up shot) for
  // Near — recognizable without a label at this button size.
  const focusPill = document.createElement('div');
  focusPill.style.cssText = 'width:34px;height:76px;border-radius:17px;background:rgba(13,17,23,.6);backdrop-filter:blur(6px);' +
    'border:1px solid rgba(255,255,255,.14);display:flex;flex-direction:column;overflow:hidden;flex-shrink:0';

  const FOCUS_ICON_FAR = '<svg width="15" height="15" viewBox="0 0 24 24" fill="none">' +
    '<circle cx="7" cy="6" r="2" fill="currentColor"/>' +
    '<path d="M3 18 L9 9 L13 14 L16 10 L21 18 Z" fill="currentColor"/></svg>';
  const FOCUS_ICON_NEAR = '<svg width="15" height="15" viewBox="0 0 24 24" fill="none">' +
    '<circle cx="12" cy="12" r="2.3" fill="currentColor"/>' +
    '<circle cx="12" cy="6" r="2.6" fill="currentColor" opacity=".8"/>' +
    '<circle cx="17" cy="9" r="2.6" fill="currentColor" opacity=".8"/>' +
    '<circle cx="17" cy="15" r="2.6" fill="currentColor" opacity=".8"/>' +
    '<circle cx="12" cy="18" r="2.6" fill="currentColor" opacity=".8"/>' +
    '<circle cx="7" cy="15" r="2.6" fill="currentColor" opacity=".8"/>' +
    '<circle cx="7" cy="9" r="2.6" fill="currentColor" opacity=".8"/></svg>';

  // speed sign: -1 near / +1 far (see SendPTZFocus's doc comment in
  // internal/auth/ptz.go) — swap these two speed values if hands-on
  // testing against real hardware shows a driver's near/far is reversed
  // from this convention; nothing else here depends on which way is which.
  [{ icon: FOCUS_ICON_FAR, speed: 1, aria: 'Focus xa (toàn cảnh)' }, { icon: FOCUS_ICON_NEAR, speed: -1, aria: 'Focus gần' }].forEach((f, i) => {
    const btn = document.createElement('button');
    btn.type = 'button';
    btn.setAttribute('aria-label', f.aria);
    btn.title = f.aria;
    btn.innerHTML = f.icon;
    btn.style.cssText = 'flex:1;border:none;background:transparent;color:#e6edf3;' +
      'display:flex;align-items:center;justify-content:center;cursor:pointer;padding:0';
    wireHoldButton(btn, () => sendFocus(f.speed), sendFocusStop);
    focusPill.appendChild(btn);
    if (i === 0) {
      const divider = document.createElement('div');
      divider.style.cssText = 'height:1px;background:rgba(255,255,255,.14)';
      focusPill.appendChild(divider);
    }
  });

  // ── Save home (overwrites preset 1 with the current position) ───────
  // A separate, plainly destructive action — never hold-to-trigger like
  // the controls above, and always confirmed first since it discards
  // whatever position was previously saved there with no way back.
  const saveBtn = document.createElement('button');
  saveBtn.type = 'button';
  saveBtn.setAttribute('aria-label', 'Lưu vị trí hiện tại vào preset 1 (Home)');
  saveBtn.title = 'Lưu làm vị trí Home';
  saveBtn.style.cssText = `width:34px;height:34px;border-radius:50%;border:1px solid ${IDLE_BORDER};background:${IDLE_BG};` +
    'color:#e6edf3;display:flex;align-items:center;justify-content:center;cursor:pointer;padding:0;flex-shrink:0';
  saveBtn.innerHTML = '<svg width="14" height="14" viewBox="0 0 24 24" fill="none">' +
    '<path d="M4 4h12l4 4v12a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1V5a1 1 0 0 1 1-1z" stroke="currentColor" stroke-width="2" stroke-linejoin="round"/>' +
    '<rect x="7" y="4" width="7" height="5" stroke="currentColor" stroke-width="2"/>' +
    '<rect x="7" y="14" width="10" height="6" stroke="currentColor" stroke-width="2"/></svg>';
  let saveBusy = false;
  saveBtn.addEventListener('click', async () => {
    if (saveBusy) return;
    if (!confirm('Lưu vị trí hiện tại vào preset 1 (Home)?\nVị trí Home cũ sẽ bị ghi đè và không thể khôi phục.')) return;
    saveBusy = true;
    saveBtn.style.background = ACTIVE_BG;
    saveBtn.style.borderColor = ACTIVE_BG;
    const ok = await sendSaveHome();
    saveBusy = false;
    saveBtn.style.background = IDLE_BG;
    saveBtn.style.borderColor = IDLE_BORDER;
    if (typeof toast === 'function') {
      toast(ok ? 'Đã lưu vị trí Home' : 'Lưu vị trí Home thất bại', ok ? 'success' : 'error');
    }
  });

  wrap.appendChild(zoomPill);
  wrap.appendChild(pad);
  wrap.appendChild(focusPill);
  wrap.appendChild(saveBtn);
  if (!container.style.position) container.style.position = 'relative';
  container.appendChild(wrap);
  container.appendChild(toggleBtn);
  applyVisibility();

  return function unmount() {
    if (activeStopFn) {
      const stopFn = activeStopFn;
      activeStopFn = null;
      stopFn();
    }
  };
}
