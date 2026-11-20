// Package otgen writes the Go binding of OpenTUI's C ABI (ot/opentui.go)
// from the pinned header (ot/opentui.h) and the binding contract
// (ot/opentui.bind.json) with the repository's C binder, internal/bindc.
// The binding is never written by hand: go generate in ot/ rewrites it,
// and TestFilesCurrent holds the committed file to this output.
package otgen

import (
	"fmt"
	"os"
	"path/filepath"

	"ergo.local/prototype/internal/bindc"
)

// Source returns the Go binding that bindc writes for the header and the
// contract in dir.
func Source(dir string) ([]byte, error) {
	header, err := os.ReadFile(filepath.Join(dir, "opentui.h"))
	if err != nil {
		return nil, err
	}
	contract, err := os.ReadFile(filepath.Join(dir, "opentui.bind.json"))
	if err != nil {
		return nil, err
	}
	m, err := bindc.Parse("opentui.h", header)
	if err != nil {
		return nil, fmt.Errorf("parse opentui.h: %v", err)
	}
	b, err := bindc.Bind(m, header, contract)
	if err != nil {
		return nil, fmt.Errorf("bind opentui.h: %v", err)
	}
	_, src, err := bindc.GoPackage(b)
	return src, err
}
