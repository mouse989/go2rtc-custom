# YOLO Counter — Build & Deploy Guide

## Quick reference — which method to use?

| Platform | GPU? | Driver / CUDA | Method | Output |
|---|---|---|---|---|
| Windows | No | Any | `setup_yolo_win.bat` (auto) | Python venv (CPU) |
| Windows | Yes | driver ≥ 522 / CUDA 11.8+ | `setup_yolo_win.bat` (auto) | Python venv (GPU) |
| Windows | Yes | driver < 522 / CUDA ≤ 11.7 | Update driver → re-run | — |
| Windows | Yes | driver 411 / CUDA 10.0 | Update driver → re-run | — |
| Windows | Yes (old way) | driver ≥ 560 | `setup_yolo_win_gpu.bat` | Python venv (cu126) |
| Linux | Yes/No | driver ≥ 525 | `setup_yolo_linux_gpu.sh` (recommended) | Python venv (`yolo_counter` wrapper) |
| Linux | Any | — | `build_yolo_linux.sh` (legacy) | single `yolo_counter` |

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
   | **11.3 – 11.7** (e.g. 11.4 / driver 475) | **456.38+** | **torch cu113 (torch 1.12.x)** |
   | < 11.3 (e.g. 10.0 / driver 411) | — | ❌ GPU not supported — see below |
   | No GPU / nvidia-smi absent | — | torch CPU-only |

3. Creates `yolo_venv\` in your deploy dir, installs torch + ultralytics
4. Verifies GPU accessibility
5. Copies `counter.py` and `yolo_counter.bat` to the deploy folder

### CUDA 11.3–11.7 (e.g. CUDA 11.4 / driver 475.14)

The script auto-selects **torch cu113** (torch 1.12.x, CUDA 11.3 runtime). GPU acceleration is **enabled** — inference and YOLO counting work correctly. Training may be slightly slower than with torch 2.x.

> **NumPy 2.x incompatibility** — torch 1.12.x was compiled against NumPy 1.x.
> If you see `RuntimeError: Numpy is not available` or `_ARRAY_API not found`,
> NumPy 2.x was pulled in by ultralytics.  Quick fix (no full re-setup needed):
> ```bat
> D:\GO_YO\yolo_venv\Scripts\pip install "numpy<2"
> ```
> The updated `setup_yolo_win.bat` now pins `numpy<2` automatically for cu113.

For better GPU support, update driver to ≥ 522.06 (enables CUDA 11.8 → torch cu118 / torch 2.x).

### If CUDA < 11.3 (e.g. CUDA 10.0, driver 411.95)

PyTorch requires CUDA 11.3 at minimum (driver ≥ 456.38) for GPU support.

The script will show a warning and offer a **CPU-only fallback** (slower but functional).

**To enable GPU:** update your NVIDIA driver:
- Minimum: **456.38** → enables CUDA 11.3 (torch cu113 / torch 1.12.x)
- Better: **522.06** → enables CUDA 11.8 (torch cu118 / torch 2.x)
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

## Linux — Python venv (recommended, no build needed)

Same idea as the Windows venv method, but for Ubuntu 20.04+. No PyInstaller, no
prebuilt binary: a small `yolo_counter` shell wrapper runs `counter.py` with a
dedicated venv's Python. go2rtc auto-discovers any executable named
`yolo_counter` in its folder and launches it directly.

### Setup (run once on the deployment machine)

> **Deploy dir must be a DIFFERENT folder from the repo checkout.** The script
> copies the wrapper to `<deploy_dir>/yolo_counter` (a *file*, no extension).
> If `<deploy_dir>` is the repo checkout itself, that path collides with the
> repo's own `yolo_counter/` *source subfolder* of the same name — `cp` can't
> replace a directory with a file there. go2rtc then finds a directory named
> `yolo_counter` and fails to launch it with a confusing
> `fork/exec: permission denied` (looks like a missing `+x` bit, but isn't).
> The script now refuses to run and explains this if it detects the collision.

```bash
# Run from inside your go2rtc-custom checkout, pointing at a SEPARATE
# deployment folder (this example: /opt/GO_YO):
chmod +x scripts/setup_yolo_linux_gpu.sh
scripts/setup_yolo_linux_gpu.sh /opt/GO_YO
```

This:
1. Picks a Python ≥ 3.9 (`python3.11`, `python3.10`, …).
2. Creates `/opt/go2rtc/yolo_venv/` and installs `torch`, `torchvision`,
   `ultralytics`, `fastapi`, `uvicorn` (CUDA 12.6 wheels by default).
3. Verifies `torch.cuda.is_available()` and a real GPU tensor allocation.
4. Copies `counter.py` and the wrapper to `/opt/go2rtc/`, deploying the wrapper
   as an executable named **`yolo_counter`** (no extension).

### Ubuntu 20.04 note

20.04 ships Python 3.8, which is too old for current PyTorch wheels. Install 3.11:

```bash
sudo add-apt-repository ppa:deadsnakes/ppa
sudo apt-get install -y python3.11 python3.11-venv
```

The setup script auto-detects and uses it.

### Older / different CUDA, or CPU-only

Override the PyTorch wheel index via `CUDA_INDEX`:

```bash
# CUDA 12.1 (driver < 525)
CUDA_INDEX=https://download.pytorch.org/whl/cu121 scripts/setup_yolo_linux_gpu.sh /opt/go2rtc
# CPU only
CUDA_INDEX=https://download.pytorch.org/whl/cpu   scripts/setup_yolo_linux_gpu.sh /opt/go2rtc
```

### Updating counter.py after code changes

```bash
cp yolo_counter/counter.py /opt/go2rtc/counter.py
```

### Remove any old PyInstaller binary

If a prebuilt `yolo_counter` from `build_yolo_linux.sh` is already in the folder,
the setup script overwrites it with the wrapper — but if you keep both around,
only one file named `yolo_counter` can exist. The wrapper is self-contained
(just needs `yolo_venv/` + `counter.py` beside it).

---

## How go2rtc discovers the launcher

**Windows** — searches in this order:

1. `yolo_counter.exe` — PyInstaller bundle or manual rename
2. `yolo_counter.bat` — Python venv wrapper ← **used by setup_yolo_win.bat**
3. `yolo_counter.cmd`

If `.exe` exists, `.bat` is ignored. Always remove or rename an old `.exe` after switching to the venv method.

**Linux** — looks for a single executable named `yolo_counter` (no extension) in
go2rtc's folder and runs it directly. This is either the `build_yolo_linux.sh`
PyInstaller binary **or** the `setup_yolo_linux_gpu.sh` venv wrapper — both use
the same filename, so only one is present at a time.

go2rtc's launcher is defensive about two common Linux deploy mistakes:
- If `yolo_counter` exists but is a **directory** (e.g. the repo's source
  subfolder copied by mistake — see the deploy-dir warning above), it's
  skipped with a clear log message instead of a confusing exec failure.
- If `yolo_counter` exists but is missing the **execute bit** (common after
  `unzip`/browser download on Unix), go2rtc `chmod +x`'s it automatically
  before launching.

---

## Notes

- GPU PyInstaller builds are **not** run in CI (no NVIDIA GPUs on GitHub runners). Build locally.
- CPU builds (`build-yolo-win64`, `build-yolo-linux64`) are available via GitHub Actions (workflow_dispatch).
- Model weights (`.pt` files) are downloaded automatically by ultralytics on first run — internet access required on the first startup.
