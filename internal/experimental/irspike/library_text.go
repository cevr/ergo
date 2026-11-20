package irspike

import (
	"errors"
	"io"
	"os"
	"path/filepath"

	"ergo.local/prototype/internal/sketch/lower"
)

// LibraryText is the Go text of the library package name that
// lower.LowerIRLibrary wrote into files: the derivative tc builds the
// package with its IR bodies (-ergoir), and name.go is printed with each
// IR body as Go (lower.IRGoText, as GoText prints: M7, the Go text is
// ergo's to print). A package with no IR body is returned as lowered. The
// text is the IR's view of what was compiled, as GoText's is (capstone
// §12 Decided 5: the linked packages take their Go from it).
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
	if _, err := Build(tc, IR, mod, filepath.Join(work, name+".a"), BuildOptions{Stderr: stderr}); err != nil {
		return nil, err
	}
	text, err := printText(tc, mod, files)
	if err != nil {
		return nil, err
	}
	for _, f := range text {
		if f.Path == name+".go" {
			return f.Data, nil
		}
	}
	return lowered, nil
}
