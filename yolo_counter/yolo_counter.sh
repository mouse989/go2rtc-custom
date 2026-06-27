#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────────────
# yolo_counter.sh  --  Python wrapper for counter.py (Linux)
#
# Deploy this file NEXT TO go2rtc as a file named "yolo_counter" (NO extension)
# with the executable bit set. setup_yolo_linux_gpu.sh does this for you.
#
# go2rtc auto-discovers an executable named "yolo_counter" in its own folder and
# launches it directly (no PyInstaller bundle / no .exe needed). This wrapper
# simply runs counter.py with the venv's Python.
# ─────────────────────────────────────────────────────────────────────────────

# Directory containing this script (resolves symlinks).
SELF="$(readlink -f "${BASH_SOURCE[0]}")"
HERE="$(dirname "$SELF")"

# Path to counter.py (default: same folder as this script).
COUNTER_PY="$HERE/counter.py"

# Python executable: prefer the venv created by setup_yolo_linux_gpu.sh.
# Exit with a clear error if the venv is missing instead of silently falling
# back to system Python (which lacks the required packages).
VENV_PYTHON="$HERE/yolo_venv/bin/python"
if [ -x "$VENV_PYTHON" ]; then
    PYTHON="$VENV_PYTHON"
else
    echo "[yolo_counter] ERROR: yolo_venv not found at $HERE/yolo_venv/" >&2
    echo "[yolo_counter] Run setup first from the repo root:" >&2
    echo "[yolo_counter]   scripts/setup_yolo_linux_gpu.sh $HERE" >&2
    exit 1
fi

# Default args used only when run manually without arguments.
# go2rtc passes its own args (--port --model --conf --rtsp-base) when it launches this.
DEFAULT_ARGS="--port 8765 --model yolo11n.pt --conf 0.35 --rtsp-base rtsp://localhost:8554"

if [ "$#" -eq 0 ]; then
    exec "$PYTHON" "$COUNTER_PY" $DEFAULT_ARGS
else
    exec "$PYTHON" "$COUNTER_PY" "$@"
fi
