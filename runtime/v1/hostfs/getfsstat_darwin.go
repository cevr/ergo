//go:build darwin

package hostfs

import (
	"math"
	"strconv"
	"syscall"
)

// Each function is whole in its own declaration (no helper).

// Getfsstat returns mounted filesystems reported by Darwin's getfsstat(2)
// without waiting for I/O completion (MNT_NOWAIT = 2).
func Getfsstat() ([]MountStat, error) {
	const (
		mntNowait      = 2
		mntRdOnly      = 0x00000001
		mntSynchronous = 0x00000002
		mntNoExec      = 0x00000004
		mntNoSuid      = 0x00000008
		mntNoDev       = 0x00000010
		mntLocal       = 0x00001000
		mntRootFS      = 0x00004000
		mntDontBrowse  = 0x00100000
		mntAutomounted = 0x00400000
	)
	var buf []syscall.Statfs_t
	var count int
	for attempt := 0; attempt < 3; attempt++ {
		n, err := syscall.Getfsstat(nil, mntNowait)
		if err != nil {
			return nil, err
		}
		if n <= 0 {
			return []MountStat{}, nil
		}
		buf = make([]syscall.Statfs_t, n+4)
		copied, err := syscall.Getfsstat(buf, mntNowait)
		if err != nil {
			return nil, err
		}
		count = copied
		if count < len(buf) {
			break
		}
	}
	if count > len(buf) {
		count = len(buf)
	}
	cString := func(raw []int8) string {
		out := make([]byte, 0, len(raw))
		for _, ch := range raw {
			if ch == 0 {
				break
			}
			out = append(out, byte(ch))
		}
		return string(out)
	}
	scaleBlocks := func(blocks uint64, blockSize uint64) int64 {
		if blockSize == 0 || blocks == 0 {
			return 0
		}
		maxU := uint64(math.MaxInt64)
		if blocks > maxU/blockSize {
			return math.MaxInt64
		}
		return int64(blocks * blockSize)
	}
	out := make([]MountStat, 0, count)
	for i := 0; i < count; i++ {
		st := buf[i]
		bsize := uint64(st.Bsize)
		total := scaleBlocks(st.Blocks, bsize)
		free := scaleBlocks(st.Bfree, bsize)
		avail := scaleBlocks(st.Bavail, bsize)
		if free > total {
			free = total
		}
		if avail > total {
			avail = total
		}
		opts := make([]string, 0, 9)
		if st.Flags&mntRdOnly != 0 {
			opts = append(opts, "ro")
		} else {
			opts = append(opts, "rw")
		}
		if st.Flags&mntSynchronous != 0 {
			opts = append(opts, "sync")
		}
		if st.Flags&mntNoExec != 0 {
			opts = append(opts, "noexec")
		}
		if st.Flags&mntNoSuid != 0 {
			opts = append(opts, "nosuid")
		}
		if st.Flags&mntNoDev != 0 {
			opts = append(opts, "nodev")
		}
		if st.Flags&mntLocal != 0 {
			opts = append(opts, "local")
		}
		if st.Flags&mntRootFS != 0 {
			opts = append(opts, "rootfs")
		}
		if st.Flags&mntDontBrowse != 0 {
			opts = append(opts, "dontbrowse")
		}
		if st.Flags&mntAutomounted != 0 {
			opts = append(opts, "automounted")
		}
		devID := strconv.FormatUint(uint64(uint32(st.Fsid.Val[0])), 10) + ":" +
			strconv.FormatUint(uint64(uint32(st.Fsid.Val[1])), 10)
		out = append(out, MountStat{
			MountPoint: cString(st.Mntonname[:]),
			Source:     cString(st.Mntfromname[:]),
			FSType:     cString(st.Fstypename[:]),
			DeviceID:   devID,
			Options:    opts,
			Total:      total,
			Free:       free,
			Available:  avail,
		})
	}
	return out, nil
}
