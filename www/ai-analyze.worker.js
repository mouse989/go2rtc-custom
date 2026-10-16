// ai-analyze.worker.js — "AI ANALYZE"/"WATCH 30S" detection pipeline.
// Runs entirely in this Web Worker, off the main UI thread: object
// detection (custom-trained YOLO, exported to ONNX, run via ONNX Runtime
// Web/WASM), vehicle/person counting, and vehicle exterior-colour
// classification (plain HSV pixel reads — no second model). The server
// never runs any of this and never sees a result; it only serves the
// published .onnx file and its label metadata
// (internal/aianalyze — GET /api/ai-analyze/model, /model/meta).
//
// 🔒 This detector never reads or stores number plates — it only counts
// vehicles/people and reads each vehicle's exterior colour.
//
// Message protocol (main thread ↔ this worker):
//   → {type:'init'}
//   ← {type:'ready', meta} | {type:'error', message}
//   → {type:'analyze', requestId, bitmap, enableTiles}   (bitmap transferred, zero-copy)
//   ← {type:'progress', requestId, pass, total, found}
//   ← {type:'result', requestId, detections, counts, coloursCount, light, ms, passes, width, height}
//   ← {type:'error', requestId, message}

importScripts('/ort.wasm.min.js');

// No cross-origin-isolation (COOP/COEP) headers are set on this server, so
// SharedArrayBuffer-backed WASM threads aren't available — force
// single-threaded execution instead of letting onnxruntime-web try and
// silently fail. A single 416x416 frame (or an occasional tile) is cheap
// enough single-threaded to stay within the spec's ~100-300ms target.
ort.env.wasm.numThreads = 1;
ort.env.wasm.simd = true;
// ort-wasm-simd-threaded.{mjs,wasm} are vendored at the site root
// alongside this worker (see www/static.go's go:embed *.wasm/*.mjs) —
// tell the runtime to look there instead of trying to derive a path from
// this worker's own (different) URL.
ort.env.wasm.wasmPaths = '/';

let _session = null;
let _modelMeta = null; // {labels, vehicleLabels, personLabels, inputSize, format, updated_at}

// A dedicated Worker has no access to window.localStorage (where the auth
// token lives — see app.js's getToken()), so the main thread must pass it
// along on every message; this worker just attaches it as a normal
// Authorization header on its own fetches, same as apiFetch() does there.
function authHeaders(token) {
  return token ? { Authorization: 'Bearer ' + token } : {};
}

async function ensureModel(token) {
  const meta = await fetch('/api/ai-analyze/model/meta', { headers: authHeaders(token) }).then(r => {
    if (!r.ok) throw new Error(r.status === 404 ? 'no AI Analyze model has been published yet' : 'failed to load AI Analyze model metadata (HTTP ' + r.status + ')');
    return r.json();
  });
  if (_session && _modelMeta && _modelMeta.updated_at === meta.updated_at) {
    return meta; // already loaded this exact published version
  }
  const modelBuf = await fetch('/api/ai-analyze/model?_=' + encodeURIComponent(meta.updated_at || ''), { headers: authHeaders(token) })
    .then(r => {
      if (!r.ok) throw new Error('failed to download AI Analyze model (HTTP ' + r.status + ')');
      return r.arrayBuffer();
    });
  _session = await ort.InferenceSession.create(modelBuf, { executionProviders: ['wasm'] });
  _modelMeta = meta;
  return meta;
}

// ── Letterbox resize + tensor conversion (spec §3.1) ────────────────────
function fitImage(width, height, inputSize) {
  const scale = Math.min(inputSize / width, inputSize / height);
  return {
    scale,
    width: Math.max(1, Math.round(width * scale)),
    height: Math.max(1, Math.round(height * scale)),
  };
}

function toInputTensor(rgba, scaledW, scaledH, inputSize, format) {
  const channels = 4;
  const planeSize = inputSize * inputSize;
  const padVal = format === 'yolox' ? 114 : 114 / 255;
  const tensor = new Float32Array(3 * planeSize).fill(padVal);
  for (let y = 0; y < scaledH; y++) {
    for (let x = 0; x < scaledW; x++) {
      const srcIdx = (y * scaledW + x) * channels;
      const dstIdx = y * inputSize + x;
      if (format === 'yolox') {
        tensor[dstIdx] = rgba[srcIdx + 2];
        tensor[planeSize + dstIdx] = rgba[srcIdx + 1];
        tensor[2 * planeSize + dstIdx] = rgba[srcIdx];
      } else {
        tensor[dstIdx] = rgba[srcIdx] / 255;
        tensor[planeSize + dstIdx] = rgba[srcIdx + 1] / 255;
        tensor[2 * planeSize + dstIdx] = rgba[srcIdx + 2] / 255;
      }
    }
  }
  return tensor;
}

// ── NMS ───────────────────────────────────────────────────────────────
function iou(a, b) {
  const [ax1, ay1, aw, ah] = a.box, [bx1, by1, bw, bh] = b.box;
  const ax2 = ax1 + aw, ay2 = ay1 + ah, bx2 = bx1 + bw, by2 = by1 + bh;
  const ix1 = Math.max(ax1, bx1), iy1 = Math.max(ay1, by1);
  const ix2 = Math.min(ax2, bx2), iy2 = Math.min(ay2, by2);
  const iw = Math.max(0, ix2 - ix1), ih = Math.max(0, iy2 - iy1);
  const inter = iw * ih;
  if (inter <= 0) return 0;
  const areaA = aw * ah, areaB = bw * bh;
  const union = areaA + areaB - inter;
  return union > 0 ? inter / union : 0;
}

function nmsByClass(dets, iouThresh, max) {
  const byClass = new Map();
  for (const d of dets) {
    if (!byClass.has(d.label)) byClass.set(d.label, []);
    byClass.get(d.label).push(d);
  }
  const out = [];
  for (const group of byClass.values()) {
    group.sort((a, b) => b.score - a.score);
    const kept = [];
    for (const cand of group) {
      if (kept.some(k => iou(k, cand) > iouThresh)) continue;
      kept.push(cand);
      if (kept.length >= max) break;
    }
    out.push(...kept);
  }
  return out;
}

// ── Output decoder for a custom Ultralytics (YOLOv8/v11) ONNX export
// (spec §3.2) — [1, 4+numClasses, numAnchors] ───────────────────────────
function decodeUltralyticsOutput(output, scale, tileSize, labels, opts) {
  const threshold = opts.threshold ?? 0.30;
  const iouT = opts.iou ?? 0.45;
  const max = opts.max ?? 200;
  const inputSize = opts.inputSize;
  const numClasses = labels.length;
  const numAnchors = (inputSize / 8) * (inputSize / 8) + (inputSize / 16) * (inputSize / 16) + (inputSize / 32) * (inputSize / 32);

  const candidates = [];
  for (let a = 0; a < numAnchors; a++) {
    let bestScore = 0, bestClassIdx = -1;
    for (let c = 0; c < numClasses; c++) {
      const score = output[(4 + c) * numAnchors + a];
      if (score > bestScore) { bestScore = score; bestClassIdx = c; }
    }
    if (bestClassIdx < 0 || bestScore < threshold) continue;

    const cx = output[0 * numAnchors + a];
    const cy = output[1 * numAnchors + a];
    const w = output[2 * numAnchors + a];
    const h = output[3 * numAnchors + a];

    const x1 = Math.max(0, (cx - w / 2) / scale);
    const y1 = Math.max(0, (cy - h / 2) / scale);
    const x2 = Math.min(tileSize.width, (cx + w / 2) / scale);
    const y2 = Math.min(tileSize.height, (cy + h / 2) / scale);

    if (x2 - x1 >= 1 && y2 - y1 >= 1) {
      candidates.push({ label: labels[bestClassIdx], score: bestScore, box: [x1, y1, x2 - x1, y2 - y1] });
    }
  }
  return nmsByClass(candidates, iouT, max);
}

// ── Multi-pass tiling (spec §3.3) — 5 regions when width >= 560px ───────
function buildScanRegions(width, height, enableTiles) {
  const full = [0, 0, width, height];
  if (!enableTiles || width < 560) return [full];
  const tileW = Math.round(0.6 * width), tileH = Math.round(0.6 * height);
  return [
    full,
    [0, 0, tileW, tileH],
    [width - tileW, 0, tileW, tileH],
    [0, height - tileH, tileW, tileH],
    [width - tileW, height - tileH, tileW, tileH],
  ];
}

// Merge detections across passes: drop boxes that touch a tile's inner
// cut edge (<=2px, passes 2-5 only — the full-frame pass or a neighboring
// tile already captured that vehicle whole), offset into image
// coordinates, then Cross-tile NMS (spec §3.3 rule 3): same label, or both
// in the "car-like" group (car/truck/bus), merged when IoU>0.45 OR one box
// sits >70% inside the other.
const CAR_LIKE = new Set(['car', 'truck', 'bus']);
function sameGroup(a, b) {
  if (a.label === b.label) return true;
  return CAR_LIKE.has(a.label.toLowerCase()) && CAR_LIKE.has(b.label.toLowerCase());
}
function containedRatio(a, b) {
  const [ax1, ay1, aw, ah] = a.box, [bx1, by1, bw, bh] = b.box;
  const ax2 = ax1 + aw, ay2 = ay1 + ah, bx2 = bx1 + bw, by2 = by1 + bh;
  const ix1 = Math.max(ax1, bx1), iy1 = Math.max(ay1, by1);
  const ix2 = Math.min(ax2, bx2), iy2 = Math.min(ay2, by2);
  const inter = Math.max(0, ix2 - ix1) * Math.max(0, iy2 - iy1);
  const areaA = aw * ah, areaB = bw * bh;
  return inter / Math.max(1, Math.min(areaA, areaB));
}

function mergeDetections(passResults, regions, imgW, imgH) {
  const all = [];
  passResults.forEach((dets, i) => {
    const [tx, ty, tw, th] = regions[i];
    const isTile = i > 0; // pass 0 is the full frame
    for (const d of dets) {
      const [x, y, w, h] = d.box;
      if (isTile) {
        const touchesLeft = tx > 0 && x <= 2;
        const touchesTop = ty > 0 && y <= 2;
        const touchesRight = tx + tw < imgW && (x + w) >= tw - 2;
        const touchesBottom = ty + th < imgH && (y + h) >= th - 2;
        if (touchesLeft || touchesTop || touchesRight || touchesBottom) continue;
      }
      all.push({ label: d.label, score: d.score, box: [x + tx, y + ty, w, h] });
    }
  });

  all.sort((a, b) => b.score - a.score);
  const kept = [];
  for (const cand of all) {
    const dup = kept.some(k => sameGroup(k, cand) && (iou(k, cand) > 0.45 || containedRatio(k, cand) > 0.70));
    if (!dup) kept.push(cand);
  }
  return kept;
}

// ── Vehicle colour: HSV + auto white-balance (spec §3.4) ────────────────
const CHROMATIC = new Set(['red', 'orange', 'yellow', 'green', 'blue', 'purple', 'brown']);

function rgbToHsv(r, g, b) {
  const max = Math.max(r, g, b), diff = max - Math.min(r, g, b);
  let hue = 0;
  if (diff !== 0) {
    if (max === r) hue = (((g - b) / diff + 6) % 6) * 60;
    else if (max === g) hue = ((b - r) / diff + 2) * 60;
    else hue = ((r - g) / diff + 4) * 60;
  }
  return [hue, max === 0 ? 0 : diff / max, max / 255];
}

function hasColourInformation(rgba, channels) {
  let satSum = 0, count = 0;
  for (let i = 0; i + 2 < rgba.length; i += 7 * channels) {
    satSum += rgbToHsv(rgba[i], rgba[i + 1], rgba[i + 2])[1];
    count++;
  }
  return count > 0 && satSum / count >= 0.06;
}

function computeWhiteBalance(rgba, channels) {
  let rSum = 0, gSum = 0, bSum = 0, count = 0;
  for (let i = 0; i + 2 < rgba.length; i += 5 * channels) {
    const [, s, v] = rgbToHsv(rgba[i], rgba[i + 1], rgba[i + 2]);
    if (s <= 0.35 && v >= 0.20 && v <= 0.95) {
      rSum += rgba[i]; gSum += rgba[i + 1]; bSum += rgba[i + 2]; count++;
    }
  }
  if (count < 50 || !rSum || !gSum || !bSum) return [1, 1, 1];
  const avg = (rSum + gSum + bSum) / 3;
  const clampGain = s => Math.min(1.35, Math.max(0.75, avg / s));
  return [clampGain(rSum), clampGain(gSum), clampGain(bSum)];
}

function classifyPixelColour(r, g, b) {
  const [h, s, v] = rgbToHsv(r, g, b);
  if (v < 0.25) return 'black';
  if (s < 0.30 || (v < 0.42 && s < 0.45)) {
    if (v > 0.78) return 'white';
    if (v > 0.58) return 'silver';
    if (v > 0.36) return 'grey';
    return 'black';
  }
  if (h < 14 || h >= 330) return 'red';
  if (h < 42) return v < 0.55 ? 'brown' : 'orange';
  if (h < 70) return 'yellow';
  if (h < 165) return 'green';
  if (h < 260) return 'blue';
  return 'purple';
}

function readVehicleColour(rgba, imgW, imgH, channels, boxInTile, wb) {
  const [bx, by, bw, bh] = boxInTile;
  const xStart = Math.max(0, Math.floor(bx + 0.15 * bw));
  const xEnd = Math.min(imgW, Math.ceil(bx + 0.85 * bw));
  const yStart = Math.max(0, Math.floor(by + 0.45 * bh));
  const yEnd = Math.min(imgH, Math.ceil(by + 0.88 * bh));

  const hist = new Map();
  let total = 0;
  for (let y = yStart; y < yEnd; y++) {
    for (let x = xStart; x < xEnd; x++) {
      const idx = (y * imgW + x) * channels;
      const c = classifyPixelColour(
        Math.min(255, rgba[idx] * wb[0]),
        Math.min(255, rgba[idx + 1] * wb[1]),
        Math.min(255, rgba[idx + 2] * wb[2]),
      );
      hist.set(c, (hist.get(c) ?? 0) + 1);
      total++;
    }
  }
  if (total < 12) return null;
  const nonBlack = total - (hist.get('black') ?? 0);
  if (nonBlack < 0.3 * total) return 'black';

  const sorted = [...hist.entries()].filter(([c]) => c !== 'black').sort((a, b) => b[1] - a[1]);
  const chromatic = sorted.find(([c]) => CHROMATIC.has(c));
  if (chromatic && chromatic[1] >= 0.4 * nonBlack) return chromatic[0];
  return sorted.find(([c]) => !CHROMATIC.has(c))?.[0] ?? 'black';
}

// ── Ambient light level — not specified numerically in the spec beyond
// "Luminance 64xN → BRIGHT/DIM/DARK"; these thresholds are this
// implementation's own reasonable choice (ITU-R BT.601 luma), not copied
// from anywhere, and were picked to stay conservative (favor DIM/DARK)
// since this is primarily consumed to flag low-light conditions where
// colour reads become unreliable, not to flag them as reliably BRIGHT. ──
function sampleLight(rgba, w, h, channels) {
  const cols = Math.min(64, w);
  const colStep = Math.max(1, Math.floor(w / cols));
  const rowStep = Math.max(1, Math.floor(h / 48));
  let sum = 0, n = 0;
  for (let y = 0; y < h; y += rowStep) {
    for (let x = 0; x < w; x += colStep) {
      const idx = (y * w + x) * channels;
      sum += 0.299 * rgba[idx] + 0.587 * rgba[idx + 1] + 0.114 * rgba[idx + 2];
      n++;
    }
  }
  const avg = n ? sum / n : 0;
  if (avg < 55) return 'DARK';
  if (avg < 110) return 'DIM';
  return 'BRIGHT';
}

// ── Orchestration ────────────────────────────────────────────────────
let _canvas = null; // OffscreenCanvas reused across calls
function getTileImageData(bitmap, region) {
  const [x, y, w, h] = region;
  if (!_canvas) _canvas = new OffscreenCanvas(w, h);
  if (_canvas.width !== w || _canvas.height !== h) { _canvas.width = w; _canvas.height = h; }
  const ctx = _canvas.getContext('2d', { willReadFrequently: true });
  ctx.clearRect(0, 0, w, h);
  ctx.drawImage(bitmap, x, y, w, h, 0, 0, w, h);
  return ctx.getImageData(0, 0, w, h);
}

async function runTile(imageData, meta) {
  const inputSize = meta.inputSize || 416;
  const { scale, width: sw, height: sh } = fitImage(imageData.width, imageData.height, inputSize);

  // Resize the tile's RGBA into the scaled letterbox area via a second
  // small canvas (nearest/linear resampling is fine at this scale — this
  // is a detector input, not a displayed image).
  let resized;
  if (sw === imageData.width && sh === imageData.height) {
    resized = imageData.data;
  } else {
    const c = new OffscreenCanvas(sw, sh);
    const ctx = c.getContext('2d');
    const srcCanvas = new OffscreenCanvas(imageData.width, imageData.height);
    srcCanvas.getContext('2d').putImageData(imageData, 0, 0);
    ctx.drawImage(srcCanvas, 0, 0, sw, sh);
    resized = ctx.getImageData(0, 0, sw, sh).data;
  }

  const tensorData = toInputTensor(resized, sw, sh, inputSize, meta.format);
  const inputName = _session.inputNames[0];
  const tensor = new ort.Tensor('float32', tensorData, [1, 3, inputSize, inputSize]);
  const outputs = await _session.run({ [inputName]: tensor });
  const outputName = _session.outputNames[0];
  const output = outputs[outputName].data;

  return decodeUltralyticsOutput(output, scale, { width: imageData.width, height: imageData.height }, meta.labels, { inputSize });
}

async function analyze(bitmap, enableTiles, onProgress, token) {
  const t0 = performance.now();
  const meta = await ensureModel(token);
  const width = bitmap.width, height = bitmap.height;
  const regions = buildScanRegions(width, height, enableTiles);

  const fullImageData = getTileImageData(bitmap, [0, 0, width, height]);
  const light = sampleLight(fullImageData.data, width, height, 4);
  const hasColour = hasColourInformation(fullImageData.data, 4);
  const wb = hasColour ? computeWhiteBalance(fullImageData.data, 4) : [1, 1, 1];

  const passResults = [];
  for (let i = 0; i < regions.length; i++) {
    const region = regions[i];
    const imageData = i === 0 ? fullImageData : getTileImageData(bitmap, region);
    const dets = await runTile(imageData, meta);
    passResults.push(dets);
    const foundSoFar = passResults.reduce((s, d) => s + d.length, 0);
    onProgress && onProgress(i + 1, regions.length, foundSoFar);
  }

  const merged = mergeDetections(passResults, regions, width, height);

  // Vehicle colour: read from the full-frame pixel buffer at the merged
  // box's own coordinates (already in full-image space).
  const vehicleSet = new Set((meta.vehicleLabels || []).map(l => l.toLowerCase()));
  for (const d of merged) {
    if (vehicleSet.has(d.label.toLowerCase()) && hasColour) {
      d.colour = readVehicleColour(fullImageData.data, width, height, 4, d.box, wb);
    } else {
      d.colour = null;
    }
  }

  const personSet = new Set((meta.personLabels || []).map(l => l.toLowerCase()));
  let vehicles = 0, people = 0;
  const byLabel = {};
  const colourCounts = {};
  for (const d of merged) {
    byLabel[d.label] = (byLabel[d.label] || 0) + 1;
    if (vehicleSet.has(d.label.toLowerCase())) vehicles++;
    else if (personSet.has(d.label.toLowerCase())) people++;
    if (d.colour) colourCounts[d.colour] = (colourCounts[d.colour] || 0) + 1;
  }

  return {
    detections: merged,
    counts: { vehicles, people, byLabel },
    coloursCount: colourCounts,
    light,
    ms: Math.round(performance.now() - t0),
    passes: regions.length,
    width, height,
    // Echoed so the HUD (main thread) can tell which byLabel keys are
    // vehicles vs. people without a separate /model/meta fetch of its own.
    vehicleLabels: meta.vehicleLabels || [],
    personLabels: meta.personLabels || [],
  };
}

self.onmessage = async (ev) => {
  const msg = ev.data;
  try {
    if (msg.type === 'init') {
      const meta = await ensureModel(msg.token);
      self.postMessage({ type: 'ready', meta });
      return;
    }
    if (msg.type === 'analyze') {
      const result = await analyze(msg.bitmap, msg.enableTiles !== false, (pass, total, found) => {
        self.postMessage({ type: 'progress', requestId: msg.requestId, pass, total, found });
      }, msg.token);
      msg.bitmap.close && msg.bitmap.close();
      self.postMessage({ type: 'result', requestId: msg.requestId, ...result });
      return;
    }
  } catch (e) {
    self.postMessage({ type: 'error', requestId: msg.requestId, message: e && e.message ? e.message : String(e) });
  }
};
