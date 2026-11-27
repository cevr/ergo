//go:build linux && arm64

package v1

import "syscall"

const (
	sysLinuxStatx   = 291
	sysLinuxFstatat = syscall.SYS_FSTATAT
)
