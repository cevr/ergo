package irspike

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"ergo.local/prototype/internal/sketch/lower"
)

// LibraryText is the Go text of the library package name that
// lower.LowerIRLibrary wrote into files: the derivative tc builds the
// package with its IR bodies (-ergoir) and prints the file name.go as it
// compiled it (-ergoirtext), each IR body as Go. A package with no IR
// body is printed by no one: its name.go is returned as lowered. The text
// is the IR's view of what was compiled, as GoText's is (capstone §12
// Decided 5: the linked packages take their Go from it).
func LibraryText(files []lower.File, tc Toolchain, name string, stderr io.Writer) ([]byte, error) {
	var lowered []byte
	for _, f := range files {
		if f.Path == name+".go" {
			lowered = f.Data
		}
	}
	if lowered == nil {
		return nil, errors.New("irspike: the library lowering wrote no " + name + ".go")
	}
	work, err := os.MkdirTemp("", "ergo-library-text-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)
	mod := filepath.Join(work, "ir")
	if err := writeModule(mod, files); err != nil {
		return nil, err
	}
	// A new directory each time: the flag is part of the go command's
	// action key, so the compile that prints always runs.
	printed := filepath.Join(work, "printed")
	if _, err := Build(tc, IR, mod, filepath.Join(work, name+".a"), BuildOptions{GCFlags: []string{"-ergoirtext=" + printed}, Stderr: stderr}); err != nil {
		return nil, err
	}
	text, err := os.ReadFile(filepath.Join(printed, name+".go"))
	if errors.Is(err, fs.ErrNotExist) {
		return lowered, nil
	}
	// A position the printer restores names the file it compiled, in the
	// temporary module: name it from the module's root, so the text does
	// not depend on the build's folder.
	return bytes.ReplaceAll(text, []byte(mod+string(filepath.Separator)), nil), err
}
