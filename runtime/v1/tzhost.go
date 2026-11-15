package v1

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// TzHostZone is the body of std tz/host's Load: the bytes of the host's
// zone file name, or "" when no zone directory holds it. It reads the
// directory $ZONEINFO names when it is one, then the zone directories of
// Go's own search on Unix (time/zoneinfo_unix.go platformZoneSources), and
// never Go's embedded or toolchain copies. A name that is empty, absolute
// or not a slash-separated path inside the directory (a .. element, a
// backslash) is "" without a read. The bytes are not checked: tz.FromTZif
// reads them.
//
//ergo:host
func TzHostZone(name string) string {
	if name == "" || !fs.ValidPath(name) || strings.ContainsAny(name, `\:`) {
		return ""
	}
	dirs := []string{"/usr/share/zoneinfo/", "/usr/share/lib/zoneinfo/", "/usr/lib/locale/TZ/", "/etc/zoneinfo/"}
	if z := os.Getenv("ZONEINFO"); z != "" {
		if info, err := os.Stat(z); err == nil && info.IsDir() {
			dirs = append([]string{z}, dirs...)
		}
	}
	for _, dir := range dirs {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
		if err == nil {
			return string(data)
		}
	}
	return ""
}
