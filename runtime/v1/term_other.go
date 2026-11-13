//go:build !linux && !darwin

package v1

import "os"

// On a platform where the runtime has no terminal control (Windows, the
// browser and WASI: no termios), no stream is a terminal. IsTerminal says
// false, Size and an Enter fail with TermNotTerminal, the typed failure a
// program already handles for a pipe, and Leave has no push to undo.
// Windows' console modes (GetConsoleMode, SetConsoleMode) are a later
// body of the same four functions.

func termEnter(int, termMode) error { return TermNotTerminal{} }

func termLeave(int) {}

func termSize(int) (cols, rows int, ok bool) { return 0, 0, false }

func fileIsTerminal(*os.File) bool { return false }
