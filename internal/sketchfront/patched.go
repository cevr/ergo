package sketchfront

import (
	"io"
	"path"

	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/toolchain"
)

// PatchedRelease is the release selected runs, with ergo's patched compile,
// built once into ergo's cache at root (internal/toolchain, Patch): the
// toolchain of every IR build (BuildRequest.IR).
func PatchedRelease(selected toolchain.Toolchain, root string, log io.Writer) (toolchain.Patched, error) {
	var goMod []byte
	for _, f := range lower.Runtime() {
		if path.Base(f.Path) == "go.mod" {
			goMod = f.Data // its go line is every generated module's
		}
	}
	source, err := toolchain.SourceOf(selected, root, goMod)
	if err != nil {
		return toolchain.Patched{}, err
	}
	return toolchain.Patch(toolchain.PatchConfig{Cache: root, Source: source, Log: log})
}
