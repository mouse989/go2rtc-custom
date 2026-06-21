# YOLO Counter — Build & Deploy Guide

## Quick reference — which method to use?

| Platform | Method | Output |
|---|---|---|
| Windows CPU (any machine) | `build_yolo_win.bat` | single `yolo_counter.exe` |
| Windows GPU, driver < 560 | `build_yolo_win_gpu.bat` | `dist\yolo_counter\` folder |
| **Windows GPU, driver ≥ 560 (CUDA 12.8 / 13.x)** | **`setup_yolo_win_gpu.bat`** | **Python venv — no .exe needed** |
| Linux | CI / `build_yolo_linux.sh` | single `yolo_counter` |

---

## Prerequisites

- Windows 10/11 (64-bit)
- Python 3.11 installed and added to PATH (`python --version` should return 3.11.x)

---

## Project structure

```
go2rtc-custom/
├── yolo_counter/
│   ├── counter.py              ← entry point
│   ├── yolo_counter.bat        ← Python wrapper (auto-discovers venv)
│   ├── pyi_rth_torch_cuda.py   ← PyInstaller runtime hook (GPU bundle only)
│   └── BUILD_GUIDE.md          ← this file
├── scripts/
│   ├── build_yolo_win.bat          ← Windows CPU — produces yolo_counter.exe
│   ├── build_yolo_win_gpu.bat      ← Windows GPU (older driver) — PyInstaller bundle
│   └── setup_yolo_win_gpu.bat      ← Windows GPU (driver 560+) — Python venv
```

---

## Windows CPU — single .exe (any machine)

```bat
scripts\build_yolo_win.bat
```

Output: `dist\yolo_counter.exe` — copy it next to `go2rtc.exe`.

---

## Windows GPU — Python venv (driver ≥ 560, CUDA 12.8 / 13.x)

> Use this when the PyInstaller GPU build fails with **WinError 1114**.
> Requires Python 3.11 installed on the **deployment machine** (the machine running go2rtc).

Run **once** on the deployment machine from the repo root:

```bat
scripts\setup_yolo_win_gpu.bat D:\GO_YO\
```

Replace `D:\GO_YO\` with wherever `go2rtc.exe` lives.

This script:
1. Creates `D:\GO_YO\yolo_venv\` with Python and installs torch (CUDA 12.6) + ultralytics
2. Verifies `torch.cuda.is_available()` returns `True` inside the venv
3. Copies `counter.py` and `yolo_counter.bat` to `D:\GO_YO\`

After setup, **remove or rename any existing `yolo_counter.exe`** in the deploy folder:
```bat
ren D:\GO_YO\yolo_counter.exe yolo_counter.exe.bak
```

go2rtc auto-discovers `yolo_counter.bat`, launches it via `cmd /c`, and passes all args.
The `.bat` detects `yolo_venv\Scripts\python.exe` automatically and uses it.

### Updating counter.py after code changes

```bat
copy yolo_counter\counter.py D:\GO_YO\counter.py
```

---

## Windows GPU — PyInstaller bundle (driver < 560 / CUDA 12.x)

> For machines with older NVIDIA drivers where the PyInstaller bundle works.

```bat
scripts\build_yolo_win_gpu.bat
```

Output **folder**: `dist\yolo_counter\`

Deploy the entire folder contents next to `go2rtc.exe`:
```bat
xcopy /E /Y dist\yolo_counter\* D:\GO_YO\
```

> **Why `--onedir` instead of `--onefile`?**
> PyTorch GPU builds contain hundreds of CUDA DLLs (`c10.dll`, `cudart64_12.dll`, `cublas64_12.dll`, …). `--onedir` keeps them all in `_internal\torch\lib\`, where torch's own loader expects them.

> **Do NOT install the `nvidia-*-cu12` pip packages.** The torch wheel is fully self-contained — those packages install newer CUDA libraries that shadow torch's bundled versions and cause WinError 1114.

---

## Placing files next to go2rtc

**venv method** — files are copied automatically by `setup_yolo_win_gpu.bat`.

**CPU / PyInstaller GPU** — copy manually:
```bat
REM CPU: single file
copy dist\yolo_counter.exe D:\GO_YO\

REM GPU PyInstaller: entire folder
xcopy /E /Y dist\yolo_counter\* D:\GO_YO\
```

---

## Notes

- The GPU PyInstaller build is **not** run in CI (no NVIDIA GPUs on GitHub-hosted runners). Build or setup locally.
- CPU builds (`build-yolo-win64` and `build-yolo-linux64`) are available via GitHub Actions (`workflow_dispatch`).
- Model weights (`.pt` files) are downloaded automatically by ultralytics on first run — internet access required on first startup.
