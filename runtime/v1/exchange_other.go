//go:build !(linux && (amd64 || arm64)) && !darwin

package v1

// exchangePaths has no atomic exchange to call here (Linux has
// renameat2(RENAME_EXCHANGE), exchange_linux.go; macOS has
// renameatx_np(RENAME_SWAP), exchange_darwin.go): an apply on this host is
// refused, never done by renames a reader could see half done.
func exchangePaths(a string, b string) error { return errNoExchange }
