//go:build darwin && (amd64 || arm64)

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
	sysDarwinGetattrlistbulk = 461

	darwinAttrBitMapCount = 5

	darwinAttrCmnName          = 0x00000001
	darwinAttrCmnDevid         = 0x00000002
	darwinAttrCmnObjtype       = 0x00000008
	darwinAttrCmnModtime       = 0x00000400
	darwinAttrCmnAcctime       = 0x00001000
	darwinAttrCmnFileid        = 0x02000000
	darwinAttrCmnError         = 0x20000000
	darwinAttrCmnReturnedAttrs = 0x80000000

	darwinAttrDirLinkcount = 0x00000001

	darwinAttrFileLinkcount     = 0x00000001
	darwinAttrFileDatalength    = 0x00000200
	darwinAttrFileDataallocsize = 0x00000400

	darwinVReg = 1
	darwinVDir = 2
	darwinVLnk = 5

	darwinDirScanBufSize = 64 * 1024
)

type darwinAttrlist struct {
	bitmapcount uint16
	reserved    uint16
	commonattr  uint32
	volattr     uint32
	dirattr     uint32
	fileattr    uint32
	forkattr    uint32
}

func callDarwinGetattrlistbulk(fd int, alist *darwinAttrlist, buf []byte) (int, syscall.Errno) {
	r1, _, errno := syscall.Syscall6(
		sysDarwinGetattrlistbulk,
		uintptr(fd),
		uintptr(unsafe.Pointer(alist)),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
		0,
		0,
	)
	runtime.KeepAlive(alist)
	runtime.KeepAlive(buf)
	return int(r1), errno
}

func scanDirPlatform(path string, scratch *FSDirScratch) ([]FSDirEntry, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, fsErr(path, &fs.PathError{Op: "open", Path: path, Err: err})
	}
	defer syscall.Close(fd)

	if len(scratch.attrBuf) < darwinDirScanBufSize {
		scratch.attrBuf = make([]byte, darwinDirScanBufSize)
	}
	buf := scratch.attrBuf
	scratch.entries = scratch.entries[:0]

	wantCmn := uint32(darwinAttrCmnName |
		darwinAttrCmnDevid |
		darwinAttrCmnObjtype |
		darwinAttrCmnModtime |
		darwinAttrCmnAcctime |
		darwinAttrCmnFileid |
		darwinAttrCmnError |
		darwinAttrCmnReturnedAttrs)
	wantFile := uint32(darwinAttrFileLinkcount | darwinAttrFileDatalength | darwinAttrFileDataallocsize)

	alist := darwinAttrlist{
		bitmapcount: darwinAttrBitMapCount,
		commonattr:  wantCmn,
		dirattr:     darwinAttrDirLinkcount,
		fileattr:    wantFile,
	}

	for {
		count, errno := callDarwinGetattrlistbulk(fd, &alist, buf)
		if errno != 0 {
			if errno == syscall.EINTR {
				continue
			}
			if errno == syscall.ENOTSUP || errno == syscall.ENOSYS || errno == syscall.EINVAL {
				return readDirLstatFallback(path, scratch)
			}
			return nil, fsErr(path, &fs.PathError{Op: "readdirent", Path: path, Err: errno})
		}
		if count <= 0 {
			break
		}
		pos := 0
		for i := 0; i < count; i++ {
			if pos+4 > len(buf) {
				return readDirLstatFallback(path, scratch)
			}
			recLen := int(binary.LittleEndian.Uint32(buf[pos : pos+4]))
			if recLen < 24 || pos+recLen > len(buf) {
				return readDirLstatFallback(path, scratch)
			}
			rec := buf[pos : pos+recLen]
			pos += recLen
			off := 4

			retCmn := binary.LittleEndian.Uint32(rec[off : off+4])
			retDir := binary.LittleEndian.Uint32(rec[off+8 : off+12])
			retFile := binary.LittleEndian.Uint32(rec[off+12 : off+16])
			off += 20

			var entryErr uint32
			if retCmn&darwinAttrCmnError != 0 {
				if off+4 > len(rec) {
					return readDirLstatFallback(path, scratch)
				}
				entryErr = binary.LittleEndian.Uint32(rec[off : off+4])
				off += 4
			}

			var nameBytes []byte
			if retCmn&darwinAttrCmnName != 0 {
				if off+8 > len(rec) {
					return readDirLstatFallback(path, scratch)
				}
				nameRefOff := off
				dataOff := int(int32(binary.LittleEndian.Uint32(rec[off : off+4])))
				dataLen := int(binary.LittleEndian.Uint32(rec[off+4 : off+8]))
				off += 8
				start := nameRefOff + dataOff
				if dataLen < 0 || start < 0 || start+dataLen > len(rec) {
					return readDirLstatFallback(path, scratch)
				}
				nameBytes = bytes.TrimRight(rec[start:start+dataLen], "\x00")
			}

			if len(nameBytes) == 0 {
				continue
			}
			if len(nameBytes) == 1 && nameBytes[0] == '.' {
				continue
			}
			if len(nameBytes) == 2 && nameBytes[0] == '.' && nameBytes[1] == '.' {
				continue
			}

			name := string(nameBytes)
			if entryErr != 0 {
				if entryErr == uint32(syscall.ENOENT) {
					continue
				}
				entry, lerr := FSLstatEntry(filepath.Join(path, name))
				if lerr != nil {
					if _, ok := lerr.(NotFound); ok {
						continue
					}
					return nil, lerr
				}
				scratch.entries = append(scratch.entries, FSDirEntry{Name: name, Entry: entry})
				continue
			}

			var dev int32
			if retCmn&darwinAttrCmnDevid != 0 {
				if off+4 > len(rec) {
					return readDirLstatFallback(path, scratch)
				}
				dev = int32(binary.LittleEndian.Uint32(rec[off : off+4]))
				off += 4
			}

			var objtype uint32
			if retCmn&darwinAttrCmnObjtype != 0 {
				if off+4 > len(rec) {
					return readDirLstatFallback(path, scratch)
				}
				objtype = binary.LittleEndian.Uint32(rec[off : off+4])
				off += 4
			}

			var modSec, modNsec int64
			if retCmn&darwinAttrCmnModtime != 0 {
				if off+16 > len(rec) {
					return readDirLstatFallback(path, scratch)
				}
				modSec = int64(binary.LittleEndian.Uint64(rec[off : off+8]))
				modNsec = int64(binary.LittleEndian.Uint64(rec[off+8 : off+16]))
				off += 16
			}

			var accSec, accNsec int64
			if retCmn&darwinAttrCmnAcctime != 0 {
				if off+16 > len(rec) {
					return readDirLstatFallback(path, scratch)
				}
				accSec = int64(binary.LittleEndian.Uint64(rec[off : off+8]))
				accNsec = int64(binary.LittleEndian.Uint64(rec[off+8 : off+16]))
				off += 16
			}

			var fileid uint64
			if retCmn&darwinAttrCmnFileid != 0 {
				if off+8 > len(rec) {
					return readDirLstatFallback(path, scratch)
				}
				fileid = binary.LittleEndian.Uint64(rec[off : off+8])
				off += 8
			}

			var dirNlink int
			if retDir&darwinAttrDirLinkcount != 0 {
				if off+4 > len(rec) {
					return readDirLstatFallback(path, scratch)
				}
				dirNlink = int(binary.LittleEndian.Uint32(rec[off : off+4]))
				off += 4
			}

			var fileNlink int
			if retFile&darwinAttrFileLinkcount != 0 {
				if off+4 > len(rec) {
					return readDirLstatFallback(path, scratch)
				}
				fileNlink = int(binary.LittleEndian.Uint32(rec[off : off+4]))
				off += 4
			}

			var dataLen int64
			if retFile&darwinAttrFileDatalength != 0 {
				if off+8 > len(rec) {
					return readDirLstatFallback(path, scratch)
				}
				dataLen = int64(binary.LittleEndian.Uint64(rec[off : off+8]))
				off += 8
			}

			var dataAlloc int64
			if retFile&darwinAttrFileDataallocsize != 0 {
				if off+8 > len(rec) {
					return readDirLstatFallback(path, scratch)
				}
				dataAlloc = int64(binary.LittleEndian.Uint64(rec[off : off+8]))
				off += 8
			}

			needCmn := uint32(darwinAttrCmnDevid | darwinAttrCmnObjtype | darwinAttrCmnModtime | darwinAttrCmnAcctime | darwinAttrCmnFileid)
			if objtype != darwinVReg || retCmn&needCmn != needCmn || retFile&wantFile != wantFile {
				entry, lerr := FSLstatEntry(filepath.Join(path, name))
				if lerr != nil {
					if _, ok := lerr.(NotFound); ok {
						continue
					}
					return nil, lerr
				}
				if dirNlink > 0 && objtype == darwinVDir && entry.Nlink == 0 {
					entry.Nlink = dirNlink
				}
				scratch.entries = append(scratch.entries, FSDirEntry{Name: name, Entry: entry})
				continue
			}

			nlink := fileNlink
			if nlink <= 0 {
				nlink = 1
			}
			scratch.entries = append(scratch.entries, FSDirEntry{
				Name: name,
				Entry: FSEntry{
					Kind:           0,
					Size:           int(dataLen),
					AllocatedBytes: int(dataAlloc),
					ModSec:         modSec,
					ModNsec:        modNsec,
					AccessSec:      accSec,
					AccessNsec:     accNsec,
					Nlink:          nlink,
					Dev:            uint64(dev),
					Ino:            fileid,
				},
			})
		}
	}

	slices.SortFunc(scratch.entries, compareFSDirEntry)
	return scratch.entries, nil
}
