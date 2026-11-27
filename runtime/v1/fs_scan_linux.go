//go:build linux && (amd64 || arm64)

package v1

import (
	"bytes"
	"encoding/binary"
	"io/fs"
	"path/filepath"
	"runtime"
	"slices"
	"syscall"
	"unsafe"
)

const (
	linuxStatxBasicStats   = 0x07ff
	linuxATSymlinkNofollow = 0x100
	linuxATStatxSyncAsStat = 0x0000
	linuxDirentNameOffset  = 19
	linuxDirScanBufSize    = 32 * 1024
)

type linuxStatxTimestamp struct {
	Sec      int64
	Nsec     uint32
	Reserved int32
}

type linuxStatx struct {
	Mask           uint32
	Blksize        uint32
	Attributes     uint64
	Nlink          uint32
	Uid            uint32
	Gid            uint32
	Mode           uint16
	spare0         uint16
	Ino            uint64
	Size           uint64
	Blocks         uint64
	AttributesMask uint64
	Atime          linuxStatxTimestamp
	Btime          linuxStatxTimestamp
	Ctime          linuxStatxTimestamp
	Mtime          linuxStatxTimestamp
	RdevMajor      uint32
	RdevMinor      uint32
	DevMajor       uint32
	DevMinor       uint32
	MntID          uint64
	DioMemAlign    uint32
	DioOffsetAlign uint32
	spare3         [12]uint64
}

func scanDirPlatform(path string, scratch *FSDirScratch) ([]FSDirEntry, error) {
	return scanDirLinux(path, scratch, false)
}

func scanDirLinux(path string, scratch *FSDirScratch, forceFstatat bool) ([]FSDirEntry, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, fsErr(path, &fs.PathError{Op: "open", Path: path, Err: err})
	}
	defer syscall.Close(fd)

	if len(scratch.dirBuf) < linuxDirScanBufSize {
		scratch.dirBuf = make([]byte, linuxDirScanBufSize)
	}
	buf := scratch.dirBuf
	scratch.entries = scratch.entries[:0]

	useFstatat := forceFstatat
	var stx linuxStatx
	var st syscall.Stat_t

	for {
		n, gerr := syscall.Getdents(fd, buf)
		if gerr != nil {
			if gerr == syscall.EINTR {
				continue
			}
			return nil, fsErr(path, &fs.PathError{Op: "readdirent", Path: path, Err: gerr})
		}
		if n == 0 {
			break
		}
		off := 0
		for off < n {
			if off+linuxDirentNameOffset >= n {
				return nil, fsErr(path, &fs.PathError{Op: "readdirent", Path: path, Err: syscall.EINVAL})
			}
			reclen := int(binary.LittleEndian.Uint16(buf[off+16 : off+18]))
			if reclen <= linuxDirentNameOffset || off+reclen > n {
				return nil, fsErr(path, &fs.PathError{Op: "readdirent", Path: path, Err: syscall.EINVAL})
			}
			nameField := buf[off+linuxDirentNameOffset : off+reclen]
			nul := bytes.IndexByte(nameField, 0)
			if nul < 0 {
				return nil, fsErr(path, &fs.PathError{Op: "readdirent", Path: path, Err: syscall.EINVAL})
			}
			nameBytes := nameField[:nul]
			nameOff := off + linuxDirentNameOffset
			off += reclen

			if len(nameBytes) == 1 && nameBytes[0] == '.' {
				continue
			}
			if len(nameBytes) == 2 && nameBytes[0] == '.' && nameBytes[1] == '.' {
				continue
			}

			var entry FSEntry
			if !useFstatat {
				errno := callLinuxStatx(fd, buf, nameOff, &stx)
				if errno == syscall.ENOSYS {
					useFstatat = true
				} else if errno == syscall.EPERM {
					if ferr := callLinuxFstatat(fd, buf, nameOff, &st); ferr == 0 {
						useFstatat = true
						fillFSEntryFromLinuxStat(&st, &entry)
						scratch.entries = append(scratch.entries, FSDirEntry{
							Name:  string(nameBytes),
							Entry: entry,
						})
						continue
					}
					childPath := filepath.Join(path, string(nameBytes))
					return nil, fsErr(childPath, &fs.PathError{Op: "lstat", Path: childPath, Err: errno})
				} else if errno == syscall.ENOENT {
					continue
				} else if errno != 0 {
					childPath := filepath.Join(path, string(nameBytes))
					return nil, fsErr(childPath, &fs.PathError{Op: "lstat", Path: childPath, Err: errno})
				} else {
					fillFSEntryFromStatx(&stx, &entry)
					scratch.entries = append(scratch.entries, FSDirEntry{
						Name:  string(nameBytes),
						Entry: entry,
					})
					continue
				}
			}

			errno := callLinuxFstatat(fd, buf, nameOff, &st)
			if errno == syscall.ENOENT {
				continue
			}
			if errno != 0 {
				childPath := filepath.Join(path, string(nameBytes))
				return nil, fsErr(childPath, &fs.PathError{Op: "lstat", Path: childPath, Err: errno})
			}
			fillFSEntryFromLinuxStat(&st, &entry)
			scratch.entries = append(scratch.entries, FSDirEntry{
				Name:  string(nameBytes),
				Entry: entry,
			})
		}
	}

	slices.SortFunc(scratch.entries, compareFSDirEntry)
	return scratch.entries, nil
}

func callLinuxStatx(dirfd int, buf []byte, nameOff int, stx *linuxStatx) syscall.Errno {
	_, _, errno := syscall.Syscall6(
		sysLinuxStatx,
		uintptr(dirfd),
		uintptr(unsafe.Pointer(&buf[nameOff])),
		uintptr(linuxATSymlinkNofollow|linuxATStatxSyncAsStat),
		uintptr(linuxStatxBasicStats),
		uintptr(unsafe.Pointer(stx)),
		0,
	)
	runtime.KeepAlive(buf)
	runtime.KeepAlive(stx)
	return errno
}

func callLinuxFstatat(dirfd int, buf []byte, nameOff int, st *syscall.Stat_t) syscall.Errno {
	_, _, errno := syscall.Syscall6(
		sysLinuxFstatat,
		uintptr(dirfd),
		uintptr(unsafe.Pointer(&buf[nameOff])),
		uintptr(unsafe.Pointer(st)),
		uintptr(linuxATSymlinkNofollow),
		0,
		0,
	)
	runtime.KeepAlive(buf)
	runtime.KeepAlive(st)
	return errno
}

func linuxEntryKind(mode uint32) int {
	switch mode & syscall.S_IFMT {
	case syscall.S_IFREG:
		return 0
	case syscall.S_IFDIR:
		return 1
	case syscall.S_IFLNK:
		return 2
	default:
		return 3
	}
}

func linuxMakeDev(major, minor uint32) uint64 {
	return (uint64(major&0x00000fff) << 8) |
		(uint64(major&0xfffff000) << 32) |
		(uint64(minor&0x000000ff) << 0) |
		(uint64(minor&0xffffff00) << 12)
}

func fillFSEntryFromStatx(stx *linuxStatx, out *FSEntry) {
	out.Kind = linuxEntryKind(uint32(stx.Mode))
	out.Size = int(stx.Size)
	if stx.Blocks > 0 {
		out.AllocatedBytes = int(int64(stx.Blocks) * 512)
	} else {
		out.AllocatedBytes = 0
	}
	out.ModSec = stx.Mtime.Sec
	out.ModNsec = int64(stx.Mtime.Nsec)
	out.AccessSec = stx.Atime.Sec
	out.AccessNsec = int64(stx.Atime.Nsec)
	out.Nlink = int(stx.Nlink)
	out.Dev = linuxMakeDev(stx.DevMajor, stx.DevMinor)
	out.Ino = stx.Ino
}

func fillFSEntryFromLinuxStat(st *syscall.Stat_t, out *FSEntry) {
	out.Kind = linuxEntryKind(st.Mode)
	out.Size = int(st.Size)
	if st.Blocks > 0 {
		out.AllocatedBytes = int(int64(st.Blocks) * 512)
	} else {
		out.AllocatedBytes = 0
	}
	out.ModSec = int64(st.Mtim.Sec)
	out.ModNsec = int64(st.Mtim.Nsec)
	out.AccessSec = int64(st.Atim.Sec)
	out.AccessNsec = int64(st.Atim.Nsec)
	out.Nlink = int(st.Nlink)
	out.Dev = uint64(st.Dev)
	out.Ino = uint64(st.Ino)
}
