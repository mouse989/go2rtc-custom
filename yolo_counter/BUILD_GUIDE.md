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

Output **folder**: `dist\yolo_counter\`

> **Why `--onedir` instead of `--onefile`?**
> PyTorch GPU builds contain hundreds of CUDA DLLs (`c10.dll`, `cudart64_121.dll`, `cublas64_12.dll`, …). With `--onefile` PyInstaller extracts them to a temporary `_MEI*` directory that Windows cannot find during DLL initialisation, causing **WinError 1114** at startup. With `--onedir` all DLLs sit next to the executable in `_internal\`, which avoids the extraction issue.

> **Why the runtime hook (`pyi_rth_torch_cuda.py`)?**
> Even with `--onedir`, CUDA DLLs from the `nvidia-*` packages land in subdirectories such as `_internal\nvidia\cuda_runtime\bin\`. Windows' DLL loader only searches the EXE directory and `%PATH%` — it does not recurse into subdirectories. The runtime hook calls `os.add_dll_directory()` for every `_internal\*` subdirectory that contains `.dll` files, making them visible to the loader **before any Python import runs**.

This script:
1. Installs PyTorch with CUDA 12.1 support
2. Installs ultralytics, opencv, fastapi, uvicorn, pyinstaller
3. Installs NVIDIA CUDA runtime pip packages (`nvidia-cuda-runtime-cu12`, `nvidia-cublas-cu12`, etc.) to ensure all CUDA DLLs are bundled by PyInstaller
4. Runs PyInstaller with `--onedir --collect-all torch --runtime-hook pyi_rth_torch_cuda.py` to place every torch DLL in the output folder and register their directories at startup

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
