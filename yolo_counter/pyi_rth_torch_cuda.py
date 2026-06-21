"""PyInstaller runtime hook — register all DLL-containing subdirs before torch loads.

Windows DLL loader only searches the EXE directory and PATH. With --onedir the
CUDA runtime DLLs land in _internal\nvidia\*\lib\ etc. which Windows never finds
on its own. os.add_dll_directory() (Python 3.8+) permanently adds a directory to
the process-wide DLL search path — but ONLY while the returned cookie object is
alive. Discarding the return value causes CPython's reference-counting GC to call
RemoveDllDirectory() immediately, making the call a no-op. We store every cookie
in a module-level list so registrations persist for the lifetime of the process.
"""
import os
import sys

_dll_dir_cookies = []  # must stay alive — destructor calls RemoveDllDirectory()

if sys.platform == 'win32' and hasattr(sys, '_MEIPASS'):
    for _root, _dirs, _files in os.walk(sys._MEIPASS):
        if any(_f.endswith('.dll') for _f in _files):
            try:
                _dll_dir_cookies.append(os.add_dll_directory(_root))
            except Exception:
                pass
