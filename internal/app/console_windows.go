package app

import (
	"os"

	"golang.org/x/sys/windows"
)

// disableQuickEdit turns off QuickEdit mode of the console window. With it
// on, a single mouse click into the window starts a text selection that
// suspends every write to the console, freezing the program until a key is
// pressed — a classic "go2rtc hangs and can't be closed" on Windows.
func disableQuickEdit() {
	h := windows.Handle(os.Stdin.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return // not a console (service, redirected input)
	}
	mode &^= windows.ENABLE_QUICK_EDIT_MODE
	mode |= windows.ENABLE_EXTENDED_FLAGS
	_ = windows.SetConsoleMode(h, mode)
}
