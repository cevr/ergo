//go:build (linux || darwin) && (amd64 || arm64)

package ot

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

var openMu sync.Mutex

// HeaderSHA256 is the sha256 of the pinned opentui.h, the contract's.
const HeaderSHA256 = "0f78783bac9d4e3206fe2cbcce1dfea00fb39953d38b4e9cdc3f1c5e46a04ab5"

// Open finds the library (Find), refuses it when an opentui.h beside it
// is not the pinned header, and loads it (Load checks ABI version 2).
// A shipped program is the binary and the library beside it, with no
// header; the header check runs where Fetch put one.
// After a successful Open, Open returns nil and loads nothing again.
func Open() error {
	openMu.Lock()
	defer openMu.Unlock()
	if loaded.Load() {
		return nil
	}
	path, err := Find()
	if err != nil {
		return err
	}
	if h, err := os.ReadFile(filepath.Join(filepath.Dir(path), "opentui.h")); err == nil {
		sum := sha256.Sum256(h)
		if got := hex.EncodeToString(sum[:]); got != HeaderSHA256 {
			return fmt.Errorf("ot: %s is beside an opentui.h with sha256 %s, not the pinned %s (OpenTUI %s); run: %s", path, got, HeaderSHA256, Version, FetchCommand)
		}
	}
	return Load(path)
}
