//go:build !linux

package task

import "os"

func cloneFile(dst, src *os.File) bool { return false }
