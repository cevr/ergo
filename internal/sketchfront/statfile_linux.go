//go:build linux

package sketchfront

import "syscall"

// statFile is name's stat (StatSums), following links.
func statFile(name string) (FileStat, bool) {
	var st syscall.Stat_t
	if err := syscall.Stat(name, &st); err != nil {
		return FileStat{}, false
	}
	return FileStat{Size: st.Size, Mtime: st.Mtim.Nano(), Ctime: st.Ctim.Nano(), Dev: uint64(st.Dev), Ino: st.Ino}, true
}
