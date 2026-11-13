package irspike

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"ergo.local/prototype/internal/sketch/lower"
)

// GoText builds files (a LowerIR or LowerIRTests module) to out with the
// derivative toolchain tc (`ergo build -go-text` passes the patched
// release of ergo's cache, internal/toolchain Patch), and writes into dir, which must not exist, the
// module as compiled: its Go files, each file that holds an IR body
// replaced by the text the derivative printed of it (-ergoirtext). The
// text is a view of what was compiled, never an input: go vet, gopls and
// a reader see the IR bodies as Go, at their .ergo positions.
func GoText(files []lower.File, tc Toolchain, out, dir string, stderr io.Writer) error {
	return goText(files, tc, out, dir, "", stderr)
}

// goText is GoText that also writes the structure of the compiled IR
// bodies (-ergoirshape) to shape when it is not "".
func goText(files []lower.File, tc Toolchain, out, dir, shape string, stderr io.Writer) error {
	if _, err := os.Stat(dir); err == nil {
		return fmt.Errorf("%s exists: the Go text goes into a new directory", dir)
	}
	work, err := os.MkdirTemp("", "ergo-go-text-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	mod := filepath.Join(work, "ir")
	if err := writeModule(mod, files); err != nil {
		return err
	}
	// A new directory each time: the flag is part of the go command's
	// action key, so the compile that prints always runs.
	printed := filepath.Join(work, "printed")
	flags := []string{"-ergoirtext=" + printed}
	if shape != "" {
		flags = append(flags, "-ergoirshape="+shape)
	}
	if _, err := Build(tc, IR, mod, out, BuildOptions{GCFlags: flags, Stderr: stderr}); err != nil {
		return err
	}
	var text []lower.File
	for _, f := range files {
		if f.Path != lower.IRFileName {
			text = append(text, f)
		}
	}
	if err := writeModule(dir, text); err != nil {
		return err
	}
	entries, err := os.ReadDir(printed)
	if err != nil {
		return err
	}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(printed, e.Name()))
		if err != nil {
			return err
		}
		// The program is the module's root package.
		if err := os.WriteFile(filepath.Join(dir, e.Name()), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}
