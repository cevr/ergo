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
//ergo:wait none
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

// TzEnv is the body of tz/host's Local's read of TZ: whether it is set,
// even to the empty string, and its value.
//
//ergo:host
//ergo:wait none
func TzEnv() (bool, string) {
	v, ok := os.LookupEnv("TZ")
	return ok, v
}

// TzHostLocal is the body of tz/host's Local's read of the host's own zone,
// /etc/localtime: the zone name its link names (the path after its last
// zoneinfo/ element) and no bytes; else the bytes of the file, which is a
// copy of a zone file; else ("", "") when there is no such file.
//
//ergo:host
//ergo:wait none
func TzHostLocal() (string, string) { return tzLocalAt("/etc/localtime") }

// tzLocalAt is TzHostLocal over the file path.
func tzLocalAt(path string) (string, string) {
	if target, err := os.Readlink(path); err == nil {
		if _, name, ok := strings.Cut(filepath.ToSlash(target), "zoneinfo/"); ok && name != "" {
			for strings.Contains(name, "zoneinfo/") {
				_, name, _ = strings.Cut(name, "zoneinfo/")
			}
			return name, ""
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", ""
	}
	return "", string(data)
}
