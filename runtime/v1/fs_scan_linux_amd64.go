//go:build linux && amd64

package v1

import "syscall"

const (
	sysLinuxStatx   = 332
	sysLinuxFstatat = syscall.SYS_NEWFSTATAT
)
