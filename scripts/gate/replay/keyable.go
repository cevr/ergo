package replay

import (
	"fmt"
	"io/fs"
)

// onlyKeyable is the rule KindOf applies, as every refusal states it.
const onlyKeyable = "only regular files, directories and symlinks can be keyed"

// Unkeyable is a file that a key would read and cannot hold: a FIFO, a
// socket, a device, or a type the walk does not know. What a step reads
// from such a file is not in its content or its stat tuple (a FIFO's buffer
// moves no stat field), so the facts that would hold it cannot key a
// replay or a record.
type Unkeyable struct {
	// Name is the path as the reader names it: relative to the checkout for
	// a checkout entry, absolute anywhere else.
	Name string
	Mode fs.FileMode
}

func (u *Unkeyable) Error() string {
	return fmt.Sprintf("%s is a %s: %s", u.Name, otherKind(u.Mode), onlyKeyable)
}

// KindOf is the one rule for what a key can read, wherever a walk reaches
// the file: an entry of the checkout (Walk), the location a link leads to
// and every path below it (Links), a path under a fingerprinted
// installation root (InstallationDigest), and a file a step receipt hashes.
// Only a regular file, a directory or a symlink can be keyed, and a symlink
// only when what it leads to can be keyed by this same rule. Any other type
// returns Other and an *Unkeyable that names the file and its type.
func KindOf(name string, mode fs.FileMode) (Kind, error) {
	switch {
	case mode.IsRegular():
		return File, nil
	case mode.IsDir():
		return Directory, nil
	case mode&fs.ModeSymlink != 0:
		return Symlink, nil
	}
	return Other, &Unkeyable{Name: name, Mode: mode}
}

// otherKind names a file type that is not a regular file, a directory or a
// symlink.
func otherKind(mode fs.FileMode) string {
	switch {
	case mode&fs.ModeNamedPipe != 0:
		return "FIFO"
	case mode&fs.ModeSocket != 0:
		return "socket"
	case mode&fs.ModeCharDevice != 0:
		return "character device"
	case mode&fs.ModeDevice != 0:
		return "device"
	}
	return "file of type " + mode.Type().String()
}
