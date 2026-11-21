package ot

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha512"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
)

// Version is the OpenTUI release the binding and the fetched library are
// pinned to. An upgrade is a unit: the header, its sha256, the contract
// and Integrity change together.
const Version = "0.9.3"

// Registry is the npm registry Fetch downloads from by default.
const Registry = "https://registry.npmjs.org"

// Integrity are the npm registry's sha512 integrity of the four prebuilt
// packages @opentui/core-<target> at Version (bindc's tier 1). Fetch
// refuses a tarball whose bytes have another.
var Integrity = map[string]string{
	"linux-x64":    "sha512-y5jVc28p2UULazGELqblXRp8TczyBUwaP/1wFcB+LwBOJuiEXgyrcgr9Iw/E+TslRYJ3RVHABz8jWGbdPiQjGg==",
	"linux-arm64":  "sha512-JFOJUwm5gg0JniigcXLeFmgTAVEgbFaLhQxRoUokROcaVgNMs3aMuBqFXpOdhH7jU3HT3cHPHP3fhrbI002fEQ==",
	"darwin-x64":   "sha512-oWquIfk4MQVed9E6eC5SYb0O+MX2MvdhVNOZCTMgagTU0fGuYaB3YRuqGVRtgammkjXmXaVFdXVYQnnTvKCEYQ==",
	"darwin-arm64": "sha512-zEyxo8tPBOXBmPx7uNZk8DuqyvxFpXO4uUxiNiX4P0zk0uQNtyrMtawryUA20iadTcUK3fr/P6DI5qQPVjvS+g==",
}

// FetchCommand is the command that puts the library where Find looks.
const FetchCommand = "go -C hosts/opentui run ./cmd/fetch"

// Target is the npm name (<os>-<cpu>) of goos/goarch, and whether a
// pinned package exists for it.
func Target(goos, goarch string) (string, bool) {
	cpu := map[string]string{"amd64": "x64", "arm64": "arm64"}[goarch]
	t := goos + "-" + cpu
	_, ok := Integrity[t]
	return t, ok && cpu != ""
}

// LibName is the library's file name on goos.
func LibName(goos string) string {
	if goos == "darwin" {
		return "libopentui.dylib"
	}
	return "libopentui.so"
}

// CacheDir is where Fetch puts the library of target by default:
// $ERGO_OPENTUI_DIR, else ~/.cache/ergo/opentui/<Version>/<target>.
func CacheDir(target string) (string, error) {
	if d := os.Getenv("ERGO_OPENTUI_DIR"); d != "" {
		return d, nil
	}
	c, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(c, "ergo", "opentui", Version, target), nil
}

// CheckIntegrity returns an error unless data has the npm integrity want
// ("sha512-<base64>").
func CheckIntegrity(data []byte, want string) error {
	sum := sha512.Sum512(data)
	if got := "sha512-" + base64.StdEncoding.EncodeToString(sum[:]); got != want {
		return fmt.Errorf("integrity %s, want %s", got, want)
	}
	return nil
}

// Fetch downloads @opentui/core-<target> at Version from registry, checks
// it against Integrity before it reads a byte of it as a tarball, and
// writes the library and opentui.h from it into dir. Nothing is written
// when the check fails.
func Fetch(client *http.Client, registry, target, dir string) error {
	want, ok := Integrity[target]
	if !ok {
		return fmt.Errorf("ot: no pinned OpenTUI %s package for %s", Version, target)
	}
	url := fmt.Sprintf("%s/@opentui/core-%s/-/core-%s-%s.tgz", registry, target, target, Version)
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("ot: fetch %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ot: fetch %s: %s", url, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return fmt.Errorf("ot: fetch %s: %w", url, err)
	}
	if err := CheckIntegrity(data, want); err != nil {
		return fmt.Errorf("ot: core-%s-%s.tgz: %w", target, Version, err)
	}
	goos := "linux"
	if len(target) > 6 && target[:6] == "darwin" {
		goos = "darwin"
	}
	files, err := untar(data, LibName(goos), "opentui.h")
	if err != nil {
		return fmt.Errorf("ot: core-%s-%s.tgz: %w", target, Version, err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for name, b := range files {
		mode := os.FileMode(0o644)
		if name != "opentui.h" {
			mode = 0o755
		}
		tmp := filepath.Join(dir, "."+name+".tmp")
		if err := os.WriteFile(tmp, b, mode); err != nil {
			return err
		}
		if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	return nil
}

// untar returns package/<name> of the gzipped tarball for each name; a
// missing one is an error.
func untar(data []byte, names ...string) (map[string][]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, n := range names {
		want["package/"+n] = true
	}
	out := map[string][]byte{}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if !want[h.Name] || h.Typeflag != tar.TypeReg {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(tr, 256<<20))
		if err != nil {
			return nil, err
		}
		out[h.Name[len("package/"):]] = b
	}
	for _, n := range names {
		if _, ok := out[n]; !ok {
			return nil, fmt.Errorf("the tarball has no package/%s", n)
		}
	}
	return out, nil
}

// Find returns the path of the library: $ERGO_OPENTUI_LIB, else the one
// beside the executable, else the one in CacheDir. It fails with one line
// that names FetchCommand when there is none.
func Find() (string, error) {
	if p := os.Getenv("ERGO_OPENTUI_LIB"); p != "" {
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("ot: ERGO_OPENTUI_LIB: %w", err)
		}
		return p, nil
	}
	name := LibName(runtime.GOOS)
	var tried []string
	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), name)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
		tried = append(tried, p)
	}
	target, ok := Target(runtime.GOOS, runtime.GOARCH)
	if ok {
		if d, err := CacheDir(target); err == nil {
			p := filepath.Join(d, name)
			if _, err := os.Stat(p); err == nil {
				return p, nil
			}
			tried = append(tried, p)
		}
	}
	return "", fmt.Errorf("ot: no OpenTUI %s library (looked at $ERGO_OPENTUI_LIB, %v); run: %s", Version, tried, FetchCommand)
}
