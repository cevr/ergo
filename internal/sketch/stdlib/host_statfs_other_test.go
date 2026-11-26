//go:build !linux

package stdlib_test

import "fmt"

func rawStatfs(path string) (total, free, avail int64, err error) {
	return 0, 0, 0, fmt.Errorf("statfs not supported in Linux /proc test helper")
}
