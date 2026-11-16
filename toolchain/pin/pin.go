// Package pin carries the pin archive lock (compile-speed plan §3.3) into
// the ergo binary: the sha256 of every archive set a release ships, which
// a fetched set must match before ergo uses a byte of it
// (internal/pinset). scripts/pinsets makes the sets and the lock's lines.
package pin

import _ "embed"

// Lock is the lock file.
//
//go:embed lock
var Lock []byte
