"""PyInstaller runtime hook — register all DLL-containing subdirs before torch loads.

Windows DLL loader only searches the EXE directory and PATH. With --onedir the
CUDA runtime DLLs land in _internal\nvidia\*\lib\ which Windows never finds on
its own. os.add_dll_directory() (Python 3.8+) permanently adds a directory to
the process-wide DLL search path. We walk the entire _MEIPASS tree and register
every directory that holds at least one DLL before any import runs.
"""
import os
import sys

if sys.platform == 'win32' and hasattr(sys, '_MEIPASS'):
    for _root, _dirs, _files in os.walk(sys._MEIPASS):
        if any(_f.endswith('.dll') for _f in _files):
            try:
                os.add_dll_directory(_root)
            except Exception:
                pass
