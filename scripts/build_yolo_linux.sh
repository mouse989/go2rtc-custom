#!/bin/bash
# Build yolo_counter for Linux on this machine.
# Run directly on the target Ubuntu server (not via Docker/container).
# Requires Python 3.11 and pip.
#
# Usage:
#   chmod +x scripts/build_yolo_linux.sh
#   ./scripts/build_yolo_linux.sh
#
# The binary will be at: dist/yolo_counter

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(dirname "$SCRIPT_DIR")"
cd "$REPO_ROOT"

echo "=== Building yolo_counter for Linux ==="
echo "Repo: $REPO_ROOT"
echo "Python: $(python3 --version 2>&1)"

# Auto-detect NVIDIA GPU and validate CUDA version (need >= 12.1 for cu121 wheels).
GPU=0
if command -v nvidia-smi &>/dev/null && nvidia-smi &>/dev/null 2>&1; then
    CUDA_VER=$(nvidia-smi 2>/dev/null | grep -oE 'CUDA Version: [0-9]+\.[0-9]+' | awk '{print $NF}')
    CUDA_MAJ=$(echo "${CUDA_VER:-0.0}" | cut -d. -f1)
    CUDA_MIN=$(echo "${CUDA_VER:-0.0}" | cut -d. -f2)
    if [ "${CUDA_MAJ:-0}" -gt 12 ] || { [ "${CUDA_MAJ:-0}" -eq 12 ] && [ "${CUDA_MIN:-0}" -ge 1 ]; }; then
        GPU=1
        echo "GPU: NVIDIA detected, CUDA $CUDA_VER → CUDA build"
    else
        echo "GPU: NVIDIA detected but CUDA $CUDA_VER < 12.1 → falling back to CPU build"
        echo "     To enable GPU, update your driver: https://www.nvidia.com/Download/index.aspx"
    fi
else
    echo "GPU: none detected → CPU-only build"
fi

# Install system libs needed by PyInstaller and OpenCV (skip if already present)
if command -v apt-get &>/dev/null; then
    echo "--- Installing system libs ---"
    sudo apt-get install -y --no-install-recommends binutils libglib2.0-0 libgl1 2>/dev/null || true
fi

echo "--- Installing Python packages ---"
if [ "$GPU" = "1" ]; then
    pip install --no-cache-dir torch --index-url https://download.pytorch.org/whl/cu121
else
    pip install torch --index-url https://download.pytorch.org/whl/cpu
fi
pip install ultralytics opencv-python-headless fastapi "uvicorn[standard]" pyinstaller

echo "--- Building binary ---"
pyinstaller --onefile --name yolo_counter yolo_counter/counter.py

echo ""
echo "=== Done! Binary: $REPO_ROOT/dist/yolo_counter ==="
if [ "$GPU" = "1" ]; then
    echo "    Variant: Linux x64 (NVIDIA GPU / CUDA)"
else
    echo "    Variant: Linux x64 (CPU-only)"
fi
