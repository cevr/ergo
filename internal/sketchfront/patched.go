package sketchfront

import (
	"io"
	"path"

	"ergo.local/prototype/internal/pinset"
	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/toolchain"
)

// PatchedRelease is the release selected runs, with ergo's patched compile,
// built once into ergo's cache at root (internal/toolchain, Patch): the
// toolchain of every IR build (BuildRequest.IR).
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
	return toolchain.Patch(toolchain.PatchConfig{Cache: root, Source: source, Log: log, Pins: pins})
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
