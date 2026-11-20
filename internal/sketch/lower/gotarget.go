package lower

import (
	"bufio"
	"bytes"
	"go/build"
	"go/build/constraint"
	"io"
	"slices"
	"strings"
)

// GoTarget is a build of the program's Go: its platform and the Go
// experiments GOEXPERIMENT turns on (LAW-BUILD-KEY). The foreign keys read
// the runtime's embedded files as the target compiles them, so a function
// of runtime/v1 whose body is one file on linux (exchange_linux.go) and
// another on darwin (exchange_darwin.go) is keyed by the file the target
// builds.
type GoTarget struct {
	GOOS, GOARCH string
	Experiments  []string
}

// ID names the target: its platform and its experiments.
func (t GoTarget) ID() string {
	return t.GOOS + "/" + t.GOARCH + " " + strings.Join(t.Experiments, ",")
}

// Compiles reports whether a build of t compiles the Go or assembly file
// name: by its name (_linux, _amd64) and its //go:build line, with cgo
// off. A build tag that is neither a platform, an experiment nor a Go
// release (race, ergoexplore, ergo_stockpark) is taken both ways: a file
// some build of t compiles is in, so the key covers every body a build of
// t may run, and two targets differ only by what their platform or
// experiments select.
func (t GoTarget) Compiles(name string, data []byte) bool {
	ctx := build.Default
	ctx.GOOS, ctx.GOARCH, ctx.CgoEnabled = t.GOOS, t.GOARCH, false
	ctx.ToolTags = slices.Clone(ctx.ToolTags)
	for _, e := range t.Experiments {
		ctx.ToolTags = append(ctx.ToolTags, "goexperiment."+e)
	}
	ctx.OpenFile = func(string) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }
	free := freeTags(data)
	if len(free) > 6 {
		return true // too many to try: the file is in, which only widens the key
	}
	for set := 0; set < 1<<len(free); set++ {
		ctx.BuildTags = nil
		for i, tag := range free {
			if set&(1<<i) != 0 {
				ctx.BuildTags = append(ctx.BuildTags, tag)
			}
		}
		if ok, err := ctx.MatchFile(".", name); err == nil && ok {
			return true
		}
	}
	return false
}

// freeTags are the tags of a file's //go:build line that name no
// platform, experiment, Go release or toolchain: sorted, each once.
func freeTags(data []byte) []string {
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "package ") {
			break
		}
		if !constraint.IsGoBuild(line) {
			continue
		}
		x, err := constraint.Parse(line)
		if err != nil {
			continue
		}
		x.Eval(func(tag string) bool {
			if !knownTag(tag) && !slices.Contains(out, tag) {
				out = append(out, tag)
			}
			return false
		})
	}
	slices.Sort(out)
	return out
}

func knownTag(tag string) bool {
	return slices.Contains(goOSes, tag) || slices.Contains(goArches, tag) || tag == "unix" || tag == "cgo" || tag == "gc" || tag == "gccgo" ||
		strings.HasPrefix(tag, "go1.") || strings.HasPrefix(tag, "goexperiment.")
}

var goOSes = []string{"aix", "android", "darwin", "dragonfly", "freebsd", "hurd", "illumos", "ios", "js", "linux", "nacl", "netbsd", "openbsd", "plan9", "solaris", "wasip1", "windows", "zos"}

var goArches = []string{"386", "amd64", "arm", "arm64", "loong64", "mips", "mipsle", "mips64", "mips64le", "ppc64", "ppc64le", "riscv64", "s390x", "sparc64", "wasm"}
