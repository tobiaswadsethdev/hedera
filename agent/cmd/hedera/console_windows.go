//go:build windows

package main

import "golang.org/x/sys/windows"

var procFreeConsole = windows.NewLazySystemDLL("kernel32.dll").NewProc("FreeConsole")

// detachConsole releases the console window Task Scheduler opens for a
// console program, so the agent runs without a window at logon. Sessions use
// their own pseudo consoles and don't need it.
func detachConsole() {
	procFreeConsole.Call()
}

// enableVTOutput makes the console interpret the escape sequences sessions
// produce (classic conhost needs this; Windows Terminal already does). It
// returns a function restoring the previous mode.
func enableVTOutput(fd uintptr) func() {
	h := windows.Handle(fd)
	var mode uint32
	if windows.GetConsoleMode(h, &mode) != nil {
		return func() {}
	}
	windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING|windows.DISABLE_NEWLINE_AUTO_RETURN)
	return func() { windows.SetConsoleMode(h, mode) }
}
