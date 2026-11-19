//go:build !(linux || darwin || dragonfly || freebsd || netbsd || openbsd)

package rt

import "errors"

// readSmall reads nothing off the systems Ergo programs run on (runtime/v1's
// unsupported_os.go): Plan 9's syscall.Open takes no mode, so the shared
// body did not compile there, and rt's error hid v1's build-tag diagnostic
// (I19 macOS check, go vet every GOOS: plan9/amd64). The pacer then keeps
// Go's own pace.
func readSmall(name string) ([]byte, error) {
	return nil, errors.New("rt: no file reads on this system")
}
