package screen

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// DriveAddr is the address a test asks the program to answer its script
// on (desktop.Drive), "" for none: $ERGO_DRIVE, or on Android, where a
// release app's environment is the system's and no launch passes it one,
// the property debug.ergo.drive, which `adb shell setprop` sets.
func DriveAddr() string {
	if a := os.Getenv("ERGO_DRIVE"); a != "" {
		return a
	}
	if runtime.GOOS != "android" {
		return ""
	}
	out, err := exec.Command("/system/bin/getprop", "debug.ergo.drive").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
