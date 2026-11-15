//go:build !unix

package toolchain

// locks says whether lock excludes other installs.
const locks = false

// lock is a no-op where flock does not exist: two installs at once each
// download, and the rename that publishes a release lets one win.
func lock(string, func()) (func(), error) { return func() {}, nil }
