# YOLO Counter — Build Guide

## Prerequisites

- Windows 10/11 (64-bit)
- Python 3.11 installed and added to PATH (`python --version` should return 3.11.x)
- For GPU build: NVIDIA GPU with CUDA 12.6 compatible driver

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

## Build — Windows GPU / NVIDIA CUDA 12.6 (recommended for production)

> Run this on a machine that has an NVIDIA GPU with driver ≥ 560 (CUDA 12.6+).
> The resulting binary uses GPU acceleration but can fall back to CPU if no GPU is found at runtime.
> **CUDA 12.6 is the minimum wheel version compatible with very new drivers (596.x, CUDA 13.x).**
> CUDA 12.6 wheels fail with WinError 1114 on those drivers because the 12.1 runtime's init routine
> is incompatible with the changed internal driver APIs in CUDA 13.x.

```bat
scripts\build_yolo_win_gpu.bat
```

Output **folder**: `dist\yolo_counter\`

> **Why `--onedir` instead of `--onefile`?**
> PyTorch GPU builds contain hundreds of CUDA DLLs (`c10.dll`, `cudart64_12.dll`, `cublas64_12.dll`, …). `--onedir` keeps them all next to the executable in `_internal\torch\lib\`, where torch's own loader expects them.

> **Critical: do NOT install the `nvidia-*-cu12` pip packages.**
> The torch **cu121 wheel is fully self-contained** — `torch\lib\` already ships its matching `cudart64_12.dll`, `cublas64_12.dll`, `cudnn*.dll`, etc. (all version 12.1). The standalone `nvidia-cuda-runtime-cu12` / `nvidia-cublas-cu12` packages install **newer** CUDA libraries (12.6/12.8). When those newer DLLs end up on the search path they get loaded *instead* of torch's bundled 12.1 libs, and the version mismatch makes the DLL's initialisation routine fail with **WinError 1114** (`ERROR_DLL_INIT_FAILED` — the DLL loads but its `DllMain` fails). The build script explicitly **uninstalls** these packages before building.

This script:
1. Uninstalls any conflicting standalone `nvidia-*-cu12` packages left from earlier attempts
2. Installs PyTorch with CUDA 12.6 support (self-contained)
3. Installs ultralytics, opencv, fastapi, uvicorn, pyinstaller
4. Verifies `import torch` works in plain Python (so a broken torch install is caught before bundling)
5. Cleans stale `dist\` / `build\` artifacts (old DLLs cause conflicts)
6. Runs PyInstaller with `--onedir --collect-all torch --collect-all ultralytics --runtime-hook pyi_rth_torch_cuda.py`

> **The runtime hook (`pyi_rth_torch_cuda.py`)** and the matching block at the top of `counter.py` call `os.add_dll_directory()` for every bundled directory that contains DLLs (keeping the returned cookies alive so the registration persists). This is a safety net so torch can always locate its own `torch\lib\` directory at startup.

## Placing the binary in the project

**GPU build** — copy the **entire contents** of `dist\yolo_counter\` (including all subfolders) to the directory where go2rtc-custom is deployed (e.g. `C:\go2rtc\`):

```bat
xcopy /E /Y dist\yolo_counter\* C:\go2rtc\
```

`yolo_counter.exe` and all its CUDA DLLs must be in the same folder as `go2rtc.exe`. Do **not** copy only the `.exe` — the DLLs alongside it are required.

**CPU build** — copy only the single `dist\yolo_counter.exe` next to `go2rtc.exe`.

## Notes

- The GPU build is **not** run in CI (GitHub Actions) because GitHub-hosted runners do not have NVIDIA GPUs. Build locally and distribute the binary manually.
- CPU builds (`build-yolo-win64` and `build-yolo-linux64`) are still available via GitHub Actions (`workflow_dispatch`) for convenience.
- Model weights (`.pt` files) are downloaded automatically by ultralytics on first run. Make sure the machine has internet access on first startup.
