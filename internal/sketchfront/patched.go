package sketchfront

import (
	"io"
	"os"
	"path"

	"ergo.local/prototype/internal/pinset"
	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/toolchain"
)

// PatchedRelease is the release selected runs, with ergo's patched compile,
// built once for the host into the shared IR compiler root (internal/
// toolchain, Patch and IRCRoot; root is ergo's cache, where the release
// lives): the toolchain of every IR build (BuildRequest.IR).
func PatchedRelease(selected toolchain.Toolchain, root string, log io.Writer) (toolchain.Patched, error) {
	return PinnedRelease(selected, root, nil, log)
}

// PinnedRelease is PatchedRelease taking the patched compile from the
// tools set of pins when it has one (docs/spec/build.md, Pin archives).
func PinnedRelease(selected toolchain.Toolchain, root string, pins *pinset.Source, log io.Writer) (toolchain.Patched, error) {
	source, err := ReleaseSource(selected, root)
	if err != nil {
		return toolchain.Patched{}, err
	}
	// The compile is the host's, shared by every process and lane, a Go
	// test's too (toolchain/irc.go, SHARED-IRC): it is not root's.
	irc, err := toolchain.IRCRoot(os.Getenv)
	if err != nil {
		return toolchain.Patched{}, err
	}
	return toolchain.Patch(toolchain.PatchConfig{Cache: irc, Source: source, Log: log, Pins: pins})
}

// ReleaseSource is the GOROOT of the pristine release selected runs in a
// generated module.
func ReleaseSource(selected toolchain.Toolchain, root string) (string, error) {
	var goMod []byte
	for _, f := range lower.Runtime() {
		if path.Base(f.Path) == "go.mod" {
			goMod = f.Data // its go line is every generated module's
		}
	}
	return toolchain.SourceOf(selected, root, goMod)
}
