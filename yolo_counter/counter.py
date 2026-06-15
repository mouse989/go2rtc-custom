#!/usr/bin/env python3
"""
YOLO-based vehicle counting service for go2rtc.

Each camera gets a dedicated background thread that:
  - Reads RTSP from go2rtc (port 8554)
  - Runs YOLO detection on each frame
  - Tracks vehicles with simple nearest-centroid tracker
  - Detects line crossings (H and V lines, configurable)
  - Exposes annotated debug JPEG + stats via HTTP API
"""

import argparse
import io
import logging
import math
import threading
import time
from collections import deque
from dataclasses import dataclass, field
from typing import Dict, List, Optional, Any

import cv2
import numpy as np
import uvicorn
from fastapi import FastAPI, HTTPException
from fastapi.responses import Response
from pydantic import BaseModel

# ---------------------------------------------------------------------------
# Logging
# ---------------------------------------------------------------------------
logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(name)s: %(message)s",
)
logger = logging.getLogger("yolo_counter")

# ---------------------------------------------------------------------------
# Global config (set at startup)
# ---------------------------------------------------------------------------
_args: argparse.Namespace = None  # type: ignore
_model = None  # ultralytics YOLO model

VEHICLE_CLASSES = {2: "car", 3: "motorcycle", 5: "bus", 7: "truck"}

# Box colours per class (BGR for cv2)
CLASS_COLORS_BGR = {
    "car":        (0,   200, 0),    # green
    "motorcycle": (200, 0,   200),  # magenta
    "bus":        (200, 200, 0),    # cyan
    "truck":      (0,   140, 200),  # orange
}

# ---------------------------------------------------------------------------
# Tracker
# ---------------------------------------------------------------------------
@dataclass
class Track:
    id: int
    cx: float
    cy: float
    prev_cx: float
    prev_cy: float
    missed: int = 0
    crossed_h: bool = False
    crossed_v: bool = False


class Tracker:
    """Greedy nearest-centroid multi-object tracker."""

    MAX_DIST = 80.0
    MAX_MISSED = 5

    def __init__(self):
        self._tracks: List[Track] = []
        self._next_id = 1

    def update(self, detections: List[tuple]) -> List[Track]:
        """
        detections: list of (cx, cy) floats
        Returns the current list of active tracks (including newly created ones).
        """
        # Mark all as missed; we'll clear the flag for matched ones
        for tr in self._tracks:
            tr.missed += 1

        unmatched_dets = list(range(len(detections)))

        for tr in self._tracks:
            if not unmatched_dets:
                break
            best_idx = None
            best_dist = self.MAX_DIST
            for idx in unmatched_dets:
                cx, cy = detections[idx]
                dist = math.hypot(cx - tr.cx, cy - tr.cy)
                if dist < best_dist:
                    best_dist = dist
                    best_idx = idx
            if best_idx is not None:
                cx, cy = detections[best_idx]
                tr.prev_cx, tr.prev_cy = tr.cx, tr.cy
                tr.cx, tr.cy = cx, cy
                tr.missed = 0
                unmatched_dets.remove(best_idx)

        # Create new tracks for unmatched detections
        for idx in unmatched_dets:
            cx, cy = detections[idx]
            self._tracks.append(Track(
                id=self._next_id,
                cx=cx, cy=cy,
                prev_cx=cx, prev_cy=cy,
            ))
            self._next_id += 1

        # Prune stale tracks
        self._tracks = [tr for tr in self._tracks if tr.missed <= self.MAX_MISSED]

        return list(self._tracks)


# ---------------------------------------------------------------------------
# Camera state
# ---------------------------------------------------------------------------
class CameraConfig(BaseModel):
    id: str
    name: str = ""
    streamName: str = ""
    lineHPos: float = 0.0
    lineVPos: float = 0.0
    countDown: bool = False
    countUp: bool = False
    countRight: bool = False
    countLeft: bool = False
    fps: float = 2.0
    tier: int = 1
    frameWidth: int = 320
    yoloConf: float = 0.35


@dataclass
class CameraState:
    config: CameraConfig
    tracker: Tracker = field(default_factory=Tracker)

    # counts
    total: int = 0
    totalDown: int = 0
    totalUp: int = 0
    totalRight: int = 0
    totalLeft: int = 0
    framesProcessed: int = 0
    lastFrameAt: float = 0.0
    startedAt: float = field(default_factory=time.time)
    lastErr: str = ""
    running: bool = True

    # debug JPEG
    debug_jpeg: bytes = b""
    debug_lock: threading.Lock = field(default_factory=threading.Lock)

    # events
    events: deque = field(default_factory=lambda: deque(maxlen=500))
    events_lock: threading.Lock = field(default_factory=threading.Lock)

    # internal
    _thread: Optional[threading.Thread] = field(default=None, init=False)
    _stop_event: threading.Event = field(default_factory=threading.Event)

    def start(self):
        self._stop_event.clear()
        self._thread = threading.Thread(target=self._run, daemon=True, name=f"cam-{self.config.id}")
        self._thread.start()

    def stop(self):
        self._stop_event.set()
        self.running = False
        if self._thread and self._thread.is_alive():
            self._thread.join(timeout=5.0)

    def _effective_fps(self) -> float:
        fps = self.config.fps if self.config.fps > 0 else 2.0
        tier = self.config.tier
        if tier == 2:
            fps /= 2.0
        elif tier >= 3:
            fps /= 4.0
        return max(fps, 0.05)

    def _run(self):
        rtsp_url = f"{_args.rtsp_base}/{self.config.streamName}"
        logger.info(f"[{self.config.id}] starting RTSP: {rtsp_url}")

        cap = None
        frame_interval = 1.0 / self._effective_fps()
        last_frame_time = 0.0

        while not self._stop_event.is_set():
            # Open/reopen capture
            if cap is None or not cap.isOpened():
                if cap is not None:
                    cap.release()
                cap = cv2.VideoCapture(rtsp_url)
                if not cap.isOpened():
                    self.lastErr = f"RTSP connect failed: {rtsp_url}"
                    logger.warning(f"[{self.config.id}] {self.lastErr}, retrying in 5s")
                    if self._stop_event.wait(5.0):
                        break
                    continue
                self.lastErr = ""
                logger.info(f"[{self.config.id}] RTSP connected")

            # FPS throttle
            now = time.monotonic()
            wait = frame_interval - (now - last_frame_time)
            if wait > 0:
                if self._stop_event.wait(wait):
                    break
                now = time.monotonic()

            ret, frame = cap.read()
            if not ret:
                self.lastErr = "RTSP read failed"
                logger.warning(f"[{self.config.id}] frame read failed, reconnecting")
                cap.release()
                cap = None
                continue

            last_frame_time = time.monotonic()
            self.lastErr = ""

            try:
                self._process_frame(frame)
            except Exception as exc:
                self.lastErr = str(exc)
                logger.exception(f"[{self.config.id}] process error")

        if cap is not None:
            cap.release()
        logger.info(f"[{self.config.id}] thread exiting")

    def _process_frame(self, frame: np.ndarray):
        cfg = self.config
        fw = cfg.frameWidth
        orig_h, orig_w = frame.shape[:2]
        scale = fw / orig_w if orig_w > 0 else 1.0
        fh = max(1, int(orig_h * scale))

        resized = cv2.resize(frame, (fw, fh))

        # YOLO inference
        results = _model(resized, conf=cfg.yoloConf, verbose=False)

        detections: List[tuple] = []   # (cx, cy)
        boxes_info: List[dict] = []    # for debug drawing

        for result in results:
            for box in result.boxes:
                cls_id = int(box.cls[0])
                if cls_id not in VEHICLE_CLASSES:
                    continue
                x1, y1, x2, y2 = map(float, box.xyxy[0])
                conf = float(box.conf[0])
                cx = (x1 + x2) / 2.0
                cy = (y1 + y2) / 2.0
                cls_name = VEHICLE_CLASSES[cls_id]
                detections.append((cx, cy))
                boxes_info.append({
                    "x1": x1, "y1": y1, "x2": x2, "y2": y2,
                    "cx": cx, "cy": cy,
                    "cls": cls_name, "conf": conf,
                })

        tracks = self.tracker.update(detections)
        self._check_crossings(tracks, fw, fh)

        self.framesProcessed += 1
        self.lastFrameAt = time.time()

        self._save_debug_jpeg(resized, boxes_info, tracks, fw, fh)

    def _check_crossings(self, tracks: List[Track], fw: int, fh: int):
        cfg = self.config
        ts = time.time()

        for tr in tracks:
            # --- Horizontal line ---
            if cfg.lineHPos > 0 and (cfg.countDown or cfg.countUp) and not tr.crossed_h:
                line_y = cfg.lineHPos * fh
                if cfg.countDown and tr.prev_cy < line_y and tr.cy >= line_y:
                    tr.crossed_h = True
                    self._emit_event(ts, "down")
                elif cfg.countUp and tr.prev_cy > line_y and tr.cy <= line_y:
                    tr.crossed_h = True
                    self._emit_event(ts, "up")

            # --- Vertical line ---
            if cfg.lineVPos > 0 and (cfg.countRight or cfg.countLeft) and not tr.crossed_v:
                line_x = cfg.lineVPos * fw
                if cfg.countRight and tr.prev_cx < line_x and tr.cx >= line_x:
                    tr.crossed_v = True
                    self._emit_event(ts, "right")
                elif cfg.countLeft and tr.prev_cx > line_x and tr.cx <= line_x:
                    tr.crossed_v = True
                    self._emit_event(ts, "left")

    def _emit_event(self, ts: float, direction: str):
        self.total += 1
        if direction == "down":
            self.totalDown += 1
        elif direction == "up":
            self.totalUp += 1
        elif direction == "right":
            self.totalRight += 1
        elif direction == "left":
            self.totalLeft += 1

        event = {
            "ts": ts,
            "cameraId": self.config.id,
            "name": self.config.name,
            "count": 1,
            "total": self.total,
            "dir": direction,
        }
        with self.events_lock:
            self.events.append(event)

        logger.info(f"[{self.config.id}] crossing {direction} total={self.total}")

    def _save_debug_jpeg(self, frame: np.ndarray, boxes_info: List[dict],
                          tracks: List[Track], fw: int, fh: int):
        dbg = frame.copy()

        # Draw bounding boxes
        for b in boxes_info:
            cls_name = b["cls"]
            color_bgr = CLASS_COLORS_BGR.get(cls_name, (0, 255, 0))
            x1, y1, x2, y2 = int(b["x1"]), int(b["y1"]), int(b["x2"]), int(b["y2"])
            cv2.rectangle(dbg, (x1, y1), (x2, y2), color_bgr, 2)
            label = f"{cls_name} {b['conf']:.2f}"
            label_y = max(y1 - 6, 12)
            cv2.putText(dbg, label, (x1, label_y),
                        cv2.FONT_HERSHEY_SIMPLEX, 0.45, color_bgr, 1, cv2.LINE_AA)

        # Draw track centroids
        cyan = (200, 200, 0)
        for tr in tracks:
            cx, cy = int(tr.cx), int(tr.cy)
            cv2.circle(dbg, (cx, cy), 5, cyan, -1)
            cv2.putText(dbg, str(tr.id), (cx + 6, cy - 4),
                        cv2.FONT_HERSHEY_SIMPLEX, 0.4, cyan, 1, cv2.LINE_AA)

        cfg = self.config
        green = (0, 200, 0)
        cyan_line = (200, 200, 0)
        white = (255, 255, 255)

        # Horizontal line
        if cfg.lineHPos > 0:
            line_y = int(cfg.lineHPos * fh)
            cv2.line(dbg, (0, line_y), (fw - 1, line_y), green, 2)
            if cfg.countDown:
                cv2.putText(dbg, "v", (fw // 4, line_y - 4),
                            cv2.FONT_HERSHEY_SIMPLEX, 0.6, white, 2, cv2.LINE_AA)
            if cfg.countUp:
                cv2.putText(dbg, "^", (fw * 3 // 4, line_y - 4),
                            cv2.FONT_HERSHEY_SIMPLEX, 0.6, white, 2, cv2.LINE_AA)

        # Vertical line
        if cfg.lineVPos > 0:
            line_x = int(cfg.lineVPos * fw)
            cv2.line(dbg, (line_x, 0), (line_x, fh - 1), cyan_line, 2)
            if cfg.countRight:
                cv2.putText(dbg, ">", (line_x + 4, fh // 4),
                            cv2.FONT_HERSHEY_SIMPLEX, 0.6, white, 2, cv2.LINE_AA)
            if cfg.countLeft:
                cv2.putText(dbg, "<", (line_x + 4, fh * 3 // 4),
                            cv2.FONT_HERSHEY_SIMPLEX, 0.6, white, 2, cv2.LINE_AA)

        # Status overlay at bottom
        effective_fps = self._effective_fps()
        overlay = (f"Total: {self.total} | FPS: {effective_fps:.1f} | "
                   f"Frames: {self.framesProcessed}")
        text_y = fh - 8
        cv2.putText(dbg, overlay, (4, text_y),
                    cv2.FONT_HERSHEY_SIMPLEX, 0.45, (0, 0, 0), 2, cv2.LINE_AA)
        cv2.putText(dbg, overlay, (4, text_y),
                    cv2.FONT_HERSHEY_SIMPLEX, 0.45, white, 1, cv2.LINE_AA)

        ok, buf = cv2.imencode(".jpg", dbg, [cv2.IMWRITE_JPEG_QUALITY, 75])
        if ok:
            with self.debug_lock:
                self.debug_jpeg = buf.tobytes()

    def snapshot(self) -> Dict[str, Any]:
        return {
            "total": self.total,
            "totalDown": self.totalDown,
            "totalUp": self.totalUp,
            "totalRight": self.totalRight,
            "totalLeft": self.totalLeft,
            "framesProcessed": self.framesProcessed,
            "lastFrameAt": self.lastFrameAt,
            "startedAt": self.startedAt,
            "lastErr": self.lastErr,
            "running": self.running and not self._stop_event.is_set(),
        }


# ---------------------------------------------------------------------------
# Global camera registry
# ---------------------------------------------------------------------------
_cameras: Dict[str, CameraState] = {}
_cameras_lock = threading.Lock()

# ---------------------------------------------------------------------------
# FastAPI app
# ---------------------------------------------------------------------------
app = FastAPI(title="YOLO Vehicle Counter")


@app.get("/health")
def health():
    with _cameras_lock:
        n = len(_cameras)
    return {
        "status": "ok",
        "model": _args.model if _args else "unknown",
        "cameras": n,
    }


@app.post("/cameras/{cam_id}")
def add_camera(cam_id: str, cam_cfg: CameraConfig):
    cam_cfg.id = cam_id
    with _cameras_lock:
        if cam_id in _cameras:
            _cameras[cam_id].stop()
            del _cameras[cam_id]
        state = CameraState(config=cam_cfg)
        _cameras[cam_id] = state
    state.start()
    logger.info(f"Registered camera {cam_id} (stream={cam_cfg.streamName})")
    return {"ok": True, "id": cam_id}


@app.delete("/cameras/{cam_id}")
def delete_camera(cam_id: str):
    with _cameras_lock:
        state = _cameras.pop(cam_id, None)
    if state is None:
        raise HTTPException(status_code=404, detail="camera not found")
    state.stop()
    logger.info(f"Unregistered camera {cam_id}")
    return {"ok": True}


@app.get("/cameras")
def list_cameras():
    with _cameras_lock:
        cams = list(_cameras.items())
    return {cam_id: state.snapshot() for cam_id, state in cams}


@app.get("/events")
def list_events(camera: str = "", since: float = 0.0, limit: int = 100):
    with _cameras_lock:
        cams = list(_cameras.values())

    result = []
    for state in cams:
        if camera and state.config.id != camera:
            continue
        with state.events_lock:
            evs = list(state.events)
        for ev in evs:
            if ev["ts"] > since:
                result.append(ev)

    # Sort newest-first
    result.sort(key=lambda e: e["ts"], reverse=True)
    return result[:limit]


@app.get("/debug/{cam_id}")
def debug_camera(cam_id: str):
    with _cameras_lock:
        state = _cameras.get(cam_id)
    if state is None:
        raise HTTPException(status_code=404, detail="camera not found")
    with state.debug_lock:
        data = state.debug_jpeg
    if not data:
        raise HTTPException(status_code=404, detail="no debug frame yet")
    return Response(content=data, media_type="image/jpeg",
                    headers={"Cache-Control": "no-cache, no-store"})


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------
def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="YOLO vehicle counting service")
    parser.add_argument("--port", type=int, default=8765, help="HTTP listen port")
    parser.add_argument("--model", default="yolo11n.pt", help="YOLO model weights")
    parser.add_argument("--conf", type=float, default=0.35, help="Detection confidence threshold")
    parser.add_argument("--rtsp-base", default="rtsp://localhost:8554",
                        help="go2rtc RTSP base URL (stream appended as /<name>)")
    return parser.parse_args()


if __name__ == "__main__":
    _args = parse_args()

    logger.info(f"Loading YOLO model: {_args.model}")
    from ultralytics import YOLO
    _model = YOLO(_args.model)
    # Warm up
    dummy = np.zeros((320, 320, 3), dtype=np.uint8)
    _model(dummy, conf=_args.conf, verbose=False)
    logger.info("Model loaded and warmed up")

    logger.info(f"Starting server on port {_args.port}")
    uvicorn.run(app, host="0.0.0.0", port=_args.port, log_level="warning")
