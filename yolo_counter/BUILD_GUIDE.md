# YOLO Counter — Build Guide

## Prerequisites

- Windows 10/11 (64-bit)
- Python 3.11 installed and added to PATH (`python --version` should return 3.11.x)
- For GPU build: NVIDIA GPU with CUDA 12.1 compatible driver

## Project structure

```
go2rtc-custom/
├── yolo_counter/
│   ├── counter.py        ← entry point
│   └── BUILD_GUIDE.md    ← this file
├── scripts/
│   ├── build_yolo_win.bat      ← Windows CPU build script
│   └── build_yolo_win_gpu.bat  ← Windows GPU (CUDA) build script
```

## Build — Windows CPU (lighter, works on any machine)

Open Command Prompt in the repo root and run:

```bat
scripts\build_yolo_win.bat
```

Output binary: `dist\yolo_counter.exe`

## Build — Windows GPU / NVIDIA CUDA 12.1 (recommended for production)

> Run this on a machine that has an NVIDIA GPU and a compatible driver installed.
> The resulting binary uses GPU acceleration but can fall back to CPU if no GPU is found at runtime.

```bat
scripts\build_yolo_win_gpu.bat
```

Output binary: `dist\yolo_counter.exe`

This script:
1. Installs PyTorch with CUDA 12.1 support
2. Installs ultralytics, opencv, fastapi, uvicorn, pyinstaller
3. Runs PyInstaller to produce a single self-contained `.exe`

## Placing the binary in the project

Copy `dist\yolo_counter.exe` to the directory where go2rtc-custom is deployed, then configure it as a worker in the Counting → Workers tab of the web UI.

## Notes

- The GPU build is **not** run in CI (GitHub Actions) because GitHub-hosted runners do not have NVIDIA GPUs. Build locally and distribute the binary manually.
- CPU builds (`build-yolo-win64` and `build-yolo-linux64`) are still available via GitHub Actions (`workflow_dispatch`) for convenience.
- Model weights (`.pt` files) are downloaded automatically by ultralytics on first run. Make sure the machine has internet access on first startup.
