//go:build !(linux && (amd64 || arm64))

package v1

// exchangePaths has no atomic exchange to call here. macOS has one,
// renamex_np(RENAME_SWAP), behind libc, which Go's syscall package does
// not bind (a follow-up of SEMTOOLS SEM4b); until then an apply on this
// host is refused, never done by renames a reader could see half done.
func exchangePaths(a string, b string) error { return errNoExchange }
