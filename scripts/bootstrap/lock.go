package main

import (
	"encoding/hex"
	"fmt"
	"os"
	"strings"
)

// lockFile is the seed lock, relative to the repository root.
const lockFile = "toolchain/seed.lock"

// seedLock is toolchain/seed.lock: the seed ergo that builds stage 1, named
// by the commit it is built from, so no seed is a trusted blob.
//
//	seed seed-0                    the seed's name and the git tag of its source
//	source <40 hex digits>         the commit the seed is built from
//	go go1.27.4                    the Go release that builds it
//	package ./cmd/ergo             the main package, relative to the source root
//	url https://…/{seed}/ergo-{goos}-{goarch}
//	                               optional: where the seed is downloaded (a
//	                               GitHub release asset); https only
//	sha256 linux/amd64 <64 hex>    the seed's digest, one line per platform
//
// Blank lines and lines that start with # are comments. Every key but
// sha256 appears once.
type seedLock struct {
	Seed      string
	Source    string
	Go        string
	Package   string
	URL       string            // "" when the seed is only rebuilt from source
	Platforms []string          // in file order
	Sums      map[string]string // platform -> sha256, lowercase hex
}

func readLock(path string) (seedLock, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return seedLock{}, err
	}
	l, err := parseLock(string(data))
	if err != nil {
		return seedLock{}, fmt.Errorf("%s: %w", path, err)
	}
	return l, nil
}

func parseLock(text string) (seedLock, error) {
	l := seedLock{Sums: map[string]string{}}
	seen := map[string]bool{}
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		key := f[0]
		bad := func(why string) error { return fmt.Errorf("line %d: %s: %q", i+1, why, line) }
		if key == "sha256" {
			if len(f) != 3 {
				return seedLock{}, bad("want sha256 GOOS/GOARCH HEX")
			}
			if !validPlatform(f[1]) {
				return seedLock{}, bad("platform is not GOOS/GOARCH")
			}
			if !isHex(f[2], 64) {
				return seedLock{}, bad("digest is not 64 lowercase hex digits")
			}
			if _, dup := l.Sums[f[1]]; dup {
				return seedLock{}, bad("platform named twice")
			}
			l.Platforms = append(l.Platforms, f[1])
			l.Sums[f[1]] = f[2]
			continue
		}
		if len(f) != 2 {
			return seedLock{}, bad("want KEY VALUE")
		}
		if seen[key] {
			return seedLock{}, bad("key named twice")
		}
		seen[key] = true
		v := f[1]
		switch key {
		case "seed":
			l.Seed = v
		case "source":
			if !isHex(v, 40) {
				return seedLock{}, bad("source is not a full commit hash")
			}
			l.Source = v
		case "go":
			if !strings.HasPrefix(v, "go1.") {
				return seedLock{}, bad("go is not a Go release name")
			}
			l.Go = v
		case "url":
			if !strings.HasPrefix(v, "https://") || !strings.Contains(v, "{goos}") || !strings.Contains(v, "{goarch}") {
				return seedLock{}, bad("url is not an https template with {goos} and {goarch}")
			}
			l.URL = v
		case "package":
			if !strings.HasPrefix(v, "./") {
				return seedLock{}, bad("package is not ./ relative")
			}
			l.Package = v
		default:
			return seedLock{}, bad("unknown key")
		}
	}
	for _, k := range []string{"seed", "source", "go", "package"} {
		if !seen[k] {
			return seedLock{}, fmt.Errorf("no %s line", k)
		}
	}
	if len(l.Platforms) == 0 {
		return seedLock{}, fmt.Errorf("no sha256 line")
	}
	return l, nil
}

// format writes the lock in the form parseLock reads, with the header that
// says how to reproduce it.
func (l seedLock) format() string {
	var b strings.Builder
	b.WriteString("# The seed ergo that builds stage 1 (docs/tooling.md, Bootstrap and release).\n")
	b.WriteString("# Reproduce: go run ./scripts/bootstrap verify-seed\n")
	fmt.Fprintf(&b, "seed %s\nsource %s\ngo %s\npackage %s\n", l.Seed, l.Source, l.Go, l.Package)
	if l.URL != "" {
		fmt.Fprintf(&b, "url %s\n", l.URL)
	}
	for _, p := range l.Platforms {
		fmt.Fprintf(&b, "sha256 %s %s\n", p, l.Sums[p])
	}
	return b.String()
}

func validPlatform(p string) bool {
	goos, goarch, ok := strings.Cut(p, "/")
	return ok && goos != "" && goarch != "" && !strings.ContainsAny(goarch, "/")
}

func isHex(s string, n int) bool {
	if len(s) != n || strings.ToLower(s) != s {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
