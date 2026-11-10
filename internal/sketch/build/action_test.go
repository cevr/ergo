package build

import (
	"strings"
	"testing"
)

// TestCommandKeepsTheUsersGoConfig: the Go build action passes the user's
// HOME, XDG_CONFIG_HOME, GOENV, AppData and GOTOOLCHAIN through unchanged,
// so the go command reads the user's own go env file and keeps its
// telemetry and other state where the user's go command does (the
// telemetry directory follows os.UserConfigDir; GOTELEMETRYDIR cannot be
// set, and TEST_TELEMETRY_DIR is an undocumented test hook). Only GOWORK,
// GOOS, GOARCH and GOFLAGS are the action's own. Each variable has exactly
// one entry, so the value the go command reads (os/exec keeps the last of
// duplicates) is the user's or the action's, never a second one.
// CGO_ENABLED is the action's too, always 0 (DIST1: the bindings are typed
// without cgo, and a cross link has no C toolchain).
func TestCommandKeepsTheUsersGoConfig(t *testing.T) {
	user := map[string]string{
		"HOME": "/users/me", "XDG_CONFIG_HOME": "/users/me/config", "GOENV": "/users/me/config/go/env",
		"AppData": `C:\Users\me\AppData\Roaming`, "GOTOOLCHAIN": "local",
	}
	for name, value := range user {
		t.Setenv(name, value)
	}
	// The action's own variables replace the user's, once each (o1).
	t.Setenv("GOWORK", "/somewhere/go.work")
	t.Setenv("GOOS", "plan9")
	t.Setenv("GOARCH", "mips")
	t.Setenv("GOFLAGS", "-race")
	t.Setenv("CGO_ENABLED", "1")
	command := Toolchain{Go: "go", GOOS: "linux", GOARCH: "arm64", GOFLAGS: "-trimpath"}.Command(Module{Dir: "/cache/modules/key"}, "/out/app")
	entries := map[string][]string{}
	for _, kv := range command.Env {
		name, value, _ := strings.Cut(kv, "=")
		entries[name] = append(entries[name], value)
	}
	want := map[string]string{"GOWORK": "off", "GOOS": "linux", "GOARCH": "arm64", "GOFLAGS": "-trimpath", "CGO_ENABLED": "0"}
	for name, value := range user {
		want[name] = value
	}
	for name, value := range want {
		if got := entries[name]; len(got) != 1 || got[0] != value {
			t.Errorf("the action's environment has %s = %q; want exactly one entry, %q", name, got, value)
		}
	}
}
