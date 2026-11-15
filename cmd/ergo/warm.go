package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/receipt"
	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketchfront"
	"ergo.local/prototype/internal/toolchain"
)

// warm is one request's warm answer (sketchfront.WarmLookup; docs/spec/
// build.md, Warm answer): `ergo build` and `ergo check` of a package
// folder answer from the store when every input they read last time still
// has its bytes. A program of one file, `-work`, and ERGO_BUILD=full take
// the normal path every time.
type warm struct {
	cache  build.Cache
	key    string
	answer sketchfront.WarmAnswer
	hit    bool
	// rec is the normal path's file system on a miss.
	rec *sketchfront.Recorder
}

// startWarm looks the request up; nil when the warm answer does not apply.
// tc is the toolchain a build (build or run) links with; check passes none.
func startWarm(opts fileOptions, tc *build.Toolchain) *warm {
	building := opts.command != "check"
	if !sketchfront.WarmEnabled() || opts.work || !isPackageFolder(opts.file) || building && tc == nil {
		return nil
	}
	source, err := receipt.LocateSource(opts.file)
	if err != nil {
		return nil
	}
	root, err := build.DefaultRoot()
	if err != nil {
		return nil
	}
	parts := []string{opts.command, source.Admitted(), warmOptions(opts)}
	if building {
		stamp, ok := goStamp(tc.Go)
		if !ok {
			return nil
		}
		parts = append(parts, stamp, tc.Root, tc.GOOS+"/"+tc.GOARCH, tc.GOFLAGS, strings.Join(tc.Tags, ","), strconv.FormatBool(tc.GoBuild)+" "+strconv.FormatBool(tc.Strip))
	}
	key := sketchfront.WarmKey(parts...)
	if key == "" {
		return nil
	}
	w := &warm{cache: build.Cache{Root: root}, key: key}
	valid := func(a sketchfront.WarmAnswer) bool {
		if !building {
			return a.Executable == ""
		}
		return a.Executable != "" && w.cache.HasExecutable(a.Executable)
	}
	w.answer, w.rec, w.hit = sketchfront.WarmLookup(w.cache, key, valid)
	return w
}

// warmToolchain is the toolchain buildAction will choose, read without
// installing anything (toolchain.Status): a managed release not yet
// installed has no go command to stamp, so its request takes the normal
// path.
func warmToolchain(opts fileOptions) *build.Toolchain {
	root, err := build.DefaultRoot()
	if err != nil {
		return nil
	}
	selected, _ := toolchain.Status(os.Getenv, root)
	tc := build.HostToolchain()
	tc.Go, tc.Root = selected.Go, selected.Root
	tc.GOOS, tc.GOARCH = opts.platform.GOOS, opts.platform.GOARCH
	return &tc
}

// warmOptions are the options that change a build's or a check's answer.
func warmOptions(opts fileOptions) string {
	return fmt.Sprintf("%q", []string{opts.target, opts.platform.String(), strconv.FormatBool(opts.locked), strconv.FormatBool(opts.json),
		opts.seed, opts.evalSteps, opts.evalBytes})
}

// goStamp names the go command: its resolved path, size and modification
// time, so a reinstalled Go is another request. It reads no byte of it.
func goStamp(goCommand string) (string, bool) {
	path, err := exec.LookPath(goCommand)
	if err != nil {
		return "", false
	}
	if path, err = filepath.EvalSymlinks(path); err != nil {
		return "", false
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", false
	}
	return fmt.Sprintf("%s %d %d", path, info.Size(), info.ModTime().UnixNano()), true
}

// fsys is the file system of the request's normal path: the Recorder on a
// miss, the host's without a warm answer.
func (w *warm) fsys() sketchfront.FS {
	if w == nil || w.rec == nil {
		return nil
	}
	return w.rec
}

// record stores the answer of a request whose normal path succeeded.
func (w *warm) record(answer sketchfront.WarmAnswer) {
	if w == nil || w.rec == nil {
		return
	}
	_ = w.rec.Record(w.cache, w.key, answer)
}

// quiet is a writer that passes through and remembers whether anything
// was written: a build that printed a note is not answered warm, since
// the answer would not print it.
type quiet struct {
	w       *os.File
	written bool
}

func (q *quiet) Write(p []byte) (int, error) {
	if len(p) > 0 {
		q.written = true
	}
	return q.w.Write(p)
}
