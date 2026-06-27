#!/usr/bin/env bash
# Setup yolo_counter GPU for Linux (Ubuntu 20.04+) using a Python venv.
# No PyInstaller build — go2rtc launches a plain "yolo_counter" wrapper script
# that runs counter.py with the venv's Python. Mirrors setup_yolo_win_gpu.bat.
#
# Usage (run from the repo root):
#   chmod +x scripts/setup_yolo_linux_gpu.sh
#   scripts/setup_yolo_linux_gpu.sh <deploy_dir>
#
#   deploy_dir: folder where the go2rtc binary lives (default: current directory)
#
# Requirements:
#   - Python 3.9+ (Ubuntu 20.04 ships 3.8 — install python3.11 via deadsnakes:
#       sudo add-apt-repository ppa:deadsnakes/ppa
#       sudo apt-get install -y python3.11 python3.11-venv)
#   - NVIDIA driver 525+ for the default CUDA 12.6 wheels.
#
# Override the CUDA wheel index if your driver is older, e.g. CUDA 12.1:
#   CUDA_INDEX=https://download.pytorch.org/whl/cu121 scripts/setup_yolo_linux_gpu.sh /opt/go2rtc
# Force CPU-only:
#   CUDA_INDEX=https://download.pytorch.org/whl/cpu scripts/setup_yolo_linux_gpu.sh /opt/go2rtc

set -e

echo "=== Setting up yolo_counter GPU venv (Linux) ==="
echo

# --- Resolve repo root and deploy dir ---
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(dirname "$SCRIPT_DIR")"
DEPLOY="${1:-$PWD}"
DEPLOY="$(cd "$DEPLOY" 2>/dev/null && pwd || echo "$DEPLOY")"

CUDA_INDEX="${CUDA_INDEX:-https://download.pytorch.org/whl/cu126}"

# --- Pick a suitable Python (>=3.9) ---
PYBIN=""
for cand in python3.12 python3.11 python3.10 python3.9 python3; do
    if command -v "$cand" &>/dev/null; then
        ver="$("$cand" -c 'import sys; print("%d%d" % sys.version_info[:2])' 2>/dev/null || echo 0)"
        if [ "${ver:-0}" -ge 39 ]; then
            PYBIN="$cand"
            break
        fi
    fi
done
if [ -z "$PYBIN" ]; then
    echo "ERROR: no Python >= 3.9 found." >&2
    echo "On Ubuntu 20.04 install python3.11:" >&2
    echo "  sudo add-apt-repository ppa:deadsnakes/ppa" >&2
    echo "  sudo apt-get install -y python3.11 python3.11-venv" >&2
    exit 1
fi
echo "[info] Using $PYBIN ($("$PYBIN" --version 2>&1))"

if [ ! -f "$REPO/yolo_counter/counter.py" ]; then
    echo "ERROR: cannot find yolo_counter/counter.py under $REPO" >&2
    echo "Run this script from inside the repository." >&2
    exit 1
fi

if [ ! -e "$DEPLOY/go2rtc" ] && [ -z "$(ls "$DEPLOY"/go2rtc* 2>/dev/null)" ]; then
    echo "WARNING: go2rtc binary not found in $DEPLOY"
    echo "Make sure you are pointing at the right deployment folder."
fi

# OpenCV runtime libs (headless still needs libgl on some minimal images).
if command -v apt-get &>/dev/null; then
    echo "--- Ensuring OpenCV system libs (libgl1, libglib2.0-0) ---"
    sudo apt-get install -y --no-install-recommends libglib2.0-0 libgl1 2>/dev/null || \
        echo "[warn] could not auto-install system libs; install libgl1 libglib2.0-0 manually if cv2 import fails"
fi

VENV="$DEPLOY/yolo_venv"
echo "--- Creating Python venv in $VENV ---"
"$PYBIN" -m venv "$VENV"

PIP="$VENV/bin/pip"
PY="$VENV/bin/python"

"$PIP" install --upgrade pip wheel >/dev/null

echo "--- Installing PyTorch + torchvision ($CUDA_INDEX) ---"
# torchvision MUST come from the same index as torch (PyPI gives CPU-only build
# that lacks CUDA NMS kernels used by ultralytics).
"$PIP" install torch torchvision --index-url "$CUDA_INDEX"

echo "--- Installing ultralytics (pulls opencv-python), web stack ---"
# Do NOT add opencv-python-headless alongside: ultralytics depends on
# opencv-python and having both can conflict.
"$PIP" install ultralytics fastapi "uvicorn[standard]"

echo "--- Verifying cv2 available ---"
if ! "$PY" -c "import cv2" 2>/dev/null; then
    echo "[warn] cv2 not importable, installing opencv-python-headless explicitly..."
    "$PIP" install opencv-python-headless
fi

echo "--- Verifying torch / torchvision / cv2 ---"
"$PY" - <<'PYEOF'
import cv2, torch, torchvision
gpu = torch.cuda.is_available()
print("cv2", cv2.__version__, "| torch", torch.__version__,
      "| torchvision", torchvision.__version__,
      "| cuda", torch.version.cuda, "| GPU:", gpu)
if gpu:
    torch.tensor([1.0], device="cuda")
    print("GPU tensor OK:", torch.cuda.get_device_name(0))
else:
    print("[warn] CUDA not available — running CPU-only. Check NVIDIA driver if a GPU is expected.")
PYEOF

echo "--- Copying counter.py and launcher to $DEPLOY ---"
cp -f "$REPO/yolo_counter/counter.py" "$DEPLOY/counter.py"
# Deploy the wrapper as "yolo_counter" (no extension) — this is the exact name
# go2rtc auto-discovers and launches on Linux.
cp -f "$REPO/yolo_counter/yolo_counter.sh" "$DEPLOY/yolo_counter"
chmod +x "$DEPLOY/yolo_counter"

echo
echo "=== Done! ==="
echo
echo "Files deployed to $DEPLOY/:"
echo "  yolo_counter        <-- go2rtc auto-discovers and launches this (executable)"
echo "  counter.py"
echo "  yolo_venv/          <-- Python venv with torch + ultralytics"
echo
echo "Restart go2rtc; logs should show:"
echo "  [counting] found yolo_counter, launching path=\"$DEPLOY/yolo_counter\""
