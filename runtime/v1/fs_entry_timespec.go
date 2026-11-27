//go:build darwin || freebsd || netbsd

package v1

import (
	"io/fs"
	"syscall"
)

func fillFSEntryStat(fi fs.FileInfo, out *FSEntry) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st == nil {
		return
	}
	out.Dev = uint64(st.Dev)
	out.Ino = uint64(st.Ino)
	out.Nlink = int(st.Nlink)
	if st.Blocks > 0 {
		out.AllocatedBytes = int(int64(st.Blocks) * 512)
	}
	out.ModSec = int64(st.Mtimespec.Sec)
	out.ModNsec = int64(st.Mtimespec.Nsec)
	out.AccessSec = int64(st.Atimespec.Sec)
	out.AccessNsec = int64(st.Atimespec.Nsec)
}
