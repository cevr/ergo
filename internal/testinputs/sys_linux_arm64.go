package testinputs

const (
	auditArch           = 0xc00000b7 // AUDIT_ARCH_AARCH64
	sysSeccomp          = 277
	sysProcessVMReadv   = 270
	x32SyscallBit       = 0
	checkX32SyscallBits = false
	// highestKnownCall is the last system call of Linux 6.12 (mseal; numbers
	// from 403 are shared by every architecture). A higher number is a call
	// this table cannot classify, so it notifies and fails closed.
	highestKnownCall = 462
)

// calls are the system calls the filter notifies about, by number. arm64 has only
// the *at forms.
var calls = []call{
	{nr: 46, kind: kindWriteFD},                               // ftruncate
	{nr: 52, kind: kindWriteFD},                               // fchmod
	{nr: 55, kind: kindWriteFD},                               // fchown
	{nr: 7, kind: kindWriteFD},                                // fsetxattr
	{nr: 16, kind: kindWriteFD},                               // fremovexattr
	{nr: 56, kind: kindOpen, dirfd: 0, path: 1, flags: 2},     // openat
	{nr: 437, kind: kindOpenat2, dirfd: 0, path: 1, flags: 2}, // openat2
	{nr: 79, kind: kindStat, dirfd: 0, path: 1, atFlags: 3},   // newfstatat
	{nr: 291, kind: kindStat, dirfd: 0, path: 1, atFlags: 2},  // statx
	{nr: 48, kind: kindStat, dirfd: 0, path: 1},               // faccessat
	{nr: 439, kind: kindStat, dirfd: 0, path: 1, atFlags: 3},  // faccessat2
	{nr: 78, kind: kindStat, dirfd: 0, path: 1},               // readlinkat
	{nr: 8, kind: kindStat, dirfd: -1, path: 0},               // getxattr
	{nr: 9, kind: kindStat, dirfd: -1, path: 0},               // lgetxattr
	{nr: 11, kind: kindStat, dirfd: -1, path: 0},              // listxattr
	{nr: 12, kind: kindStat, dirfd: -1, path: 0},              // llistxattr
	{nr: 43, kind: kindStat, dirfd: -1, path: 0},              // statfs
	{nr: 264, kind: kindStat, dirfd: 0, path: 1, atFlags: 4},  // name_to_handle_at
	{nr: 27, kind: kindStat, dirfd: -1, path: 1},              // inotify_add_watch
	// Writes whose outcome depends on what exists, recorded as lookups.
	{nr: 34, kind: kindStat, dirfd: 0, path: 1, write: writeWhole},                                                                                     // mkdirat
	{nr: 33, kind: kindStat, dirfd: 0, path: 1, write: writeWhole},                                                                                     // mknodat
	{nr: 35, kind: kindStat, dirfd: 0, path: 1, write: writeWhole},                                                                                     // unlinkat
	{nr: 36, kind: kindStat, dirfd: 1, path: 2, write: writeWhole},                                                                                     // symlinkat: the link's path
	{nr: 37, kind: kindStat, dirfd: 0, path: 1, atFlags: 4, second: true, dirfd2: 2, path2: 3, write: writeRead, write2: writeWhole},                   // linkat
	{nr: 38, kind: kindStat, dirfd: 0, path: 1, second: true, dirfd2: 2, path2: 3, write: writeMove, write2: writeWhole},                               // renameat
	{nr: 45, kind: kindStat, dirfd: -1, path: 0, write: writePart},                                                                                     // truncate
	{nr: 53, kind: kindStat, dirfd: 0, path: 1, write: writePart},                                                                                      // fchmodat
	{nr: 54, kind: kindStat, dirfd: 0, path: 1, atFlags: 4, write: writePart},                                                                          // fchownat
	{nr: 88, kind: kindStat, dirfd: 0, path: 1, atFlags: 3, write: writePart, name: "utimensat"},                                                       // utimensat
	{nr: 276, kind: kindStat, dirfd: 0, path: 1, second: true, dirfd2: 2, path2: 3, write: writeMove, write2: writeWhole, flags: 4, name: "renameat2"}, // renameat2
	{nr: 452, kind: kindStat, dirfd: 0, path: 1, atFlags: 3, write: writePart},                                                                         // fchmodat2
	{nr: 5, kind: kindStat, dirfd: -1, path: 0, write: writePart},                                                                                      // setxattr
	{nr: 6, kind: kindStat, dirfd: -1, path: 0, write: writePart},                                                                                      // lsetxattr
	{nr: 14, kind: kindStat, dirfd: -1, path: 0, write: writePart},                                                                                     // removexattr
	{nr: 15, kind: kindStat, dirfd: -1, path: 0, write: writePart},                                                                                     // lremovexattr
	{nr: 49, kind: kindChdir, dirfd: -1, path: 0},                                                                                                      // chdir
	{nr: 203, kind: kindSocket, path: 1, flags: 2},                                                                                                     // connect
	{nr: 200, kind: kindSocket, path: 1, flags: 2, name: "bind"},                                                                                       // bind
	{nr: 206, kind: kindSocket, path: 4, flags: 5},                                                                                                     // sendto
	{nr: 211, kind: kindSocketMsg, path: 1},                                                                                                            // sendmsg
	{nr: 269, kind: kindSocketMsgs, path: 1, flags: 2},                                                                                                 // sendmmsg
	{nr: 221, kind: kindExec, dirfd: -1, path: 0, env: 2},                                                                                              // execve
	{nr: 281, kind: kindExec, dirfd: 0, path: 1, env: 3, atFlags: 4},                                                                                   // execveat
	{nr: 435, kind: kindClone3},                                                                                                                        // clone3
	{nr: 277, kind: kindSeccomp},                                                                                                                       // seccomp
	{nr: 51, kind: kindUnobservable, name: "chroot"},
	{nr: 41, kind: kindUnobservable, name: "pivot_root"},
	{nr: 97, kind: kindUnobservable, name: "unshare"},
	{nr: 268, kind: kindUnobservable, name: "setns"},
	{nr: 40, kind: kindUnobservable, name: "mount"},
	{nr: 39, kind: kindUnobservable, name: "umount2"},
	{nr: 428, kind: kindUnobservable, name: "open_tree"}, // the new mount API
	{nr: 429, kind: kindUnobservable, name: "move_mount"},
	{nr: 430, kind: kindUnobservable, name: "fsopen"},
	{nr: 432, kind: kindUnobservable, name: "fsmount"},
	{nr: 433, kind: kindUnobservable, name: "fspick"},
	{nr: 442, kind: kindUnobservable, name: "mount_setattr"},
	{nr: 265, kind: kindUnobservable, name: "open_by_handle_at"},
	{nr: 263, kind: kindUnobservable, name: "fanotify_mark"}, // watches a path by events, not lookups
	{nr: 60, kind: kindUnobservable, name: "quotactl"},       // opens the quota file a path names
	{nr: 443, kind: kindUnobservable, name: "quotactl_fd"},
	{nr: 280, kind: kindUnobservable, name: "bpf"}, // opens pinned objects by path
	{nr: 425, kind: kindRefused, name: "io_uring_setup"},
	{nr: 220, kind: kindNamespaceClone, name: "clone"},
}
