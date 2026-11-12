package main

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// goTool is the go command a build runs and the GOTOOLCHAIN it runs with.
type goTool struct {
	cmd       string // the go command: "go" (found on PATH) or an absolute path
	toolchain string // GOTOOLCHAIN: a pinned release name, or "local"
}

// selectGo reads ERGO_GO as cmd/ergo does: unset or "managed" is the pinned
// release (the go command on PATH switches to it through GOTOOLCHAIN, which
// fetches it from the module proxy and checks it against the checksum
// database), "system" is the go on PATH as it is, and any other value is the
// absolute path of a go command.
func selectGo(environ []string, pinned string) (goTool, error) {
	switch v := lookup(environ, "ERGO_GO"); v {
	case "", "managed":
		return goTool{cmd: "go", toolchain: pinned}, nil
	case "system":
		return goTool{cmd: "go", toolchain: "local"}, nil
	default:
		if !filepath.IsAbs(v) {
			return goTool{}, fmt.Errorf("ERGO_GO=%q: want managed, system or an absolute path to a go command", v)
		}
		return goTool{cmd: v, toolchain: "local"}, nil
	}
}

// passed are the variables of the caller's environment a build sees. They
// locate programs, caches and the module proxy, or tune the go process; none
// of them changes the bytes go build writes. Everything else is dropped, and
// buildEnv fixes the variables that do change them.
var passed = []string{
	"PATH", "HOME", "TMPDIR", "GOTMPDIR", "XDG_CACHE_HOME",
	"GOCACHE", "GOMODCACHE", "GOPATH", "GOPROXY",
	"GOMAXPROCS", "GOMEMLIMIT", "GOGC",
}

// buildEnv is the environment of a go build for goos/goarch: the passed
// variables of environ, and fixed values for everything that reaches the
// binary. GOENV=off ignores the user's go env file, GOWORK=off a go.work
// above the tree, GOFLAGS is empty, and CGO_ENABLED=0 makes the binary the
// same whether or not the host has a C compiler. GOSUMDB is never passed, so
// a fetched Go release is always checked against the default checksum
// database.
func buildEnv(environ []string, tool goTool, goos, goarch, cache string) []string {
	var env []string
	for _, kv := range environ {
		k, _, _ := strings.Cut(kv, "=")
		for _, p := range passed {
			if k == p {
				env = append(env, kv)
			}
		}
	}
	if cache != "" {
		env = append(env, "GOCACHE="+cache)
	}
	return append(env,
		"GOENV=off", "GOWORK=off", "GOFLAGS=", "CGO_ENABLED=0",
		"GOOS="+goos, "GOARCH="+goarch, "GOTOOLCHAIN="+tool.toolchain)
}

// goBuild is one go build of pkg in dir for platform, written to out.
type goBuild struct {
	environ  []string // the caller's environment, filtered by buildEnv
	tool     goTool
	dir      string // the module root
	pkg      string // a ./ package path
	platform string // GOOS/GOARCH
	out      string
	cache    string // a GOCACHE of its own, or "" for the caller's
}

func (b goBuild) run() error {
	goos, goarch, _ := strings.Cut(b.platform, "/")
	// -trimpath keeps the checkout's path out of the binary, and
	// -buildvcs=false its git state: a build is a function of the source
	// files and the Go release.
	cmd := exec.Command(b.tool.cmd, "build", "-trimpath", "-buildvcs=false", "-o", b.out, b.pkg)
	cmd.Dir = b.dir
	cmd.Env = buildEnv(b.environ, b.tool, goos, goarch, b.cache)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("go build %s for %s in %s: %v\n%s", b.pkg, b.platform, b.dir, err, out.String())
	}
	return nil
}

// goVersion is the Go release that built the binary at path.
func goVersion(path string) (string, error) {
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return "", err
	}
	return info.GoVersion, nil
}

func fileSum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func lookup(environ []string, key string) string {
	v := ""
	for _, kv := range environ {
		if k, val, ok := strings.Cut(kv, "="); ok && k == key {
			v = val
		}
	}
	return v
}

func git(repo string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

// export writes the tree of commit into dst, an empty directory: a clean
// checkout with no git state, from git archive.
func export(repo, commit, dst string) error {
	cmd := exec.Command("git", "-C", repo, "archive", "--format=tar", commit)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	r, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	exErr := untar(r, dst)
	if exErr != nil {
		_, _ = io.Copy(io.Discard, r)
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("git archive %s: %v: %s", commit, err, strings.TrimSpace(stderr.String()))
	}
	return exErr
}

func untar(r io.Reader, dst string) error {
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if h.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		name := strings.TrimSuffix(h.Name, "/")
		if !filepath.IsLocal(name) {
			return fmt.Errorf("git archive: %q is outside the tree", h.Name)
		}
		p := filepath.Join(dst, filepath.FromSlash(name))
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(0o644)
			if h.Mode&0o111 != 0 {
				mode = 0o755
			}
			f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
			if err != nil {
				return err
			}
			_, err = io.Copy(f, tr)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(h.Linkname, p); err != nil {
				return err
			}
		default:
			return fmt.Errorf("git archive: %q has type %q", h.Name, h.Typeflag)
		}
	}
}
