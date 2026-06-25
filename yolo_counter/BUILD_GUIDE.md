# YOLO Counter — Build & Deploy Guide

## Quick reference — which method to use?

| Platform | GPU? | Driver / CUDA | Method | Output |
|---|---|---|---|---|
| Windows | No | Any | `setup_yolo_win.bat` (auto) | Python venv (CPU) |
| Windows | Yes | driver ≥ 522 / CUDA 11.8+ | `setup_yolo_win.bat` (auto) | Python venv (GPU) |
| Windows | Yes | driver < 522 / CUDA ≤ 11.7 | Update driver → re-run | — |
| Windows | Yes | driver 411 / CUDA 10.0 | Update driver → re-run | — |
| Windows | Yes (old way) | driver ≥ 560 | `setup_yolo_win_gpu.bat` | Python venv (cu126) |
| Linux | Any | — | CI / `build_yolo_linux.sh` | single `yolo_counter` |

> **Recommended path for all Windows machines:** `setup_yolo_win.bat` — it auto-detects your CUDA version and installs the correct PyTorch.

---

## Prerequisites

- Windows 10/11 (64-bit)
- Python 3.11 installed and added to PATH (`python --version` should return 3.11.x)
- NVIDIA GPU with driver ≥ 522.06 for GPU mode (optional)

---

## Project structure

```
go2rtc-custom/
├── yolo_counter/
│   ├── counter.py              ← entry point
│   ├── yolo_counter.bat        ← Python wrapper (auto-discovers venv)
│   ├── pyi_rth_torch_cuda.py   ← PyInstaller runtime hook (legacy GPU bundle)
│   └── BUILD_GUIDE.md          ← this file
├── scripts/
│   ├── setup_yolo_win.bat          ← ★ RECOMMENDED — auto-detect CUDA + install venv
│   ├── setup_yolo_win_gpu.bat      ← Legacy: hardcoded cu126 (driver ≥ 560 only)
│   ├── build_yolo_win.bat          ← CPU — produces yolo_counter.exe (PyInstaller)
│   └── build_yolo_win_gpu.bat      ← GPU — PyInstaller bundle (older drivers)
```

---

## Windows — Python venv (recommended for all GPU/CPU setups)

Run **once** on the deployment machine from the repository root:

```bat
scripts\setup_yolo_win.bat D:\GO_YO\
```

Replace `D:\GO_YO\` with the folder where `go2rtc.exe` lives.

### What the script does

1. Detects your NVIDIA driver and CUDA version via `nvidia-smi`
2. Selects the matching PyTorch build:

   | CUDA shown in nvidia-smi | Minimum driver | PyTorch build |
   |---|---|---|
   | 12.6 or 13.x | 561.09+ | torch cu126 |
   | 12.1 – 12.5 | 530.xx+ | torch cu121 |
   | 11.8 – 12.0 | 522.06+ | torch cu118 |
   | < 11.8 (e.g. 10.0, 11.3) | — | ❌ GPU not supported — see below |
   | No GPU / nvidia-smi absent | — | torch CPU-only |

3. Creates `yolo_venv\` in your deploy dir, installs torch + ultralytics
4. Verifies GPU accessibility
5. Copies `counter.py` and `yolo_counter.bat` to the deploy folder

### If CUDA < 11.8 (e.g. CUDA 10.0, driver 411.95)

PyTorch 2.x and ultralytics YOLO11 require **CUDA 11.8 at minimum** (driver ≥ 522.06).

The script will show a warning and offer a **CPU-only fallback** (slower but functional).

**To enable GPU:** update your NVIDIA driver:
- Minimum: **522.06** → enables CUDA 11.8 (torch cu118)
- Recommended: **561.09+** → enables CUDA 12.6 (torch cu126, best performance)
- Download: https://www.nvidia.com/Download/index.aspx

> **Note:** if your GPU is Kepler architecture (GTX 600 / GTX 700 series), the
> maximum supported driver is 391.35 (CUDA 9.1). These GPUs **cannot** run YOLO11
> on GPU regardless of driver version. Use CPU-only mode.

### After setup — remove old yolo_counter.exe

go2rtc prefers `.exe` over `.bat`. If an old `yolo_counter.exe` exists in the deploy folder, rename or remove it:

```bat
ren D:\GO_YO\yolo_counter.exe yolo_counter.exe.bak
```

### Updating counter.py after code changes

```bat
copy yolo_counter\counter.py D:\GO_YO\counter.py
```

---

## Windows — CPU only (PyInstaller, single .exe)

Use when you don't have a GPU or don't want to install Python on the deployment machine.

```bat
scripts\build_yolo_win.bat
```

Output: `dist\yolo_counter.exe` — copy it next to `go2rtc.exe`. No Python needed on target machine.

---

## Windows GPU — PyInstaller bundle (legacy, driver < 560)

> The venv approach above is recommended over PyInstaller GPU builds.
> Use this only if you cannot install Python on the deployment machine and
> your driver is < 560.

```bat
scripts\build_yolo_win_gpu.bat
```

Output folder: `dist\yolo_counter\`

Deploy the entire folder contents next to `go2rtc.exe`:
```bat
xcopy /E /Y dist\yolo_counter\* D:\GO_YO\
```

> **Why `--onedir` instead of `--onefile`?**
> PyTorch GPU builds contain hundreds of CUDA DLLs. `--onedir` keeps them in
> `_internal\torch\lib\` where torch's loader expects them.
>
> **Do NOT install the `nvidia-*-cu12` pip packages.** The torch wheel bundles
> its own CUDA DLLs — standalone nvidia packages install mismatched versions
> that cause WinError 1114.

---

## How go2rtc discovers the launcher

go2rtc searches for `yolo_counter` in this order:

1. `yolo_counter.exe` — PyInstaller bundle or manual rename
2. `yolo_counter.bat` — Python venv wrapper ← **used by setup_yolo_win.bat**
3. `yolo_counter.cmd`

If `.exe` exists, `.bat` is ignored. Always remove or rename an old `.exe` after switching to the venv method.

---

## Notes

- GPU PyInstaller builds are **not** run in CI (no NVIDIA GPUs on GitHub runners). Build locally.
- CPU builds (`build-yolo-win64`, `build-yolo-linux64`) are available via GitHub Actions (workflow_dispatch).
- Model weights (`.pt` files) are downloaded automatically by ultralytics on first run — internet access required on the first startup.
