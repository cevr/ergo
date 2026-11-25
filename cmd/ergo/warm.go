package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
	cache build.Cache
	key   string
	// parts are the request's key parts but the extra ones (recordAs).
	parts  []string
	answer sketchfront.WarmAnswer
	hit    bool
	// rec is the normal path's file system on a miss.
	rec *sketchfront.Recorder
}

// startWarm looks the request up; nil when the warm answer does not apply.
// tc is the toolchain a build (build or run) or a test links with; check
// passes none. extra are parts of the request read outside the front end
// (a test's law ledger).
func startWarm(opts fileOptions, tc *build.Toolchain, extra ...string) *warm {
	w, valid := prepareWarm(opts, tc, extra...)
	if w == nil {
		return nil
	}
	w.answer, w.rec, w.hit = sketchfront.WarmLookup(w.cache, w.key, valid)
	return w
}

// prepareWarm is the request's key and store, and the check its stored
// answer must pass; nil when the warm answer does not apply (-work,
// -report copies and -go-text print or write what only a build that runs
// makes).
func prepareWarm(opts fileOptions, tc *build.Toolchain, extra ...string) (*warm, func(sketchfront.WarmAnswer) bool) {
	building := opts.command == "build" || opts.command == "run"
	linking := opts.command != "check"
	// A recursive request (`ergo test ./...`, CS8) is the folder it walks.
	folder, command := opts.file, opts.command
	if recursivePattern(folder) {
		folder, command = filepath.Dir(filepath.Clean(folder)), command+" ./..."
	} else if !isPackageFolder(folder) {
		return nil, nil
	}
	if !sketchfront.WarmEnabled() || opts.work || opts.report != "" || opts.goText != "" || linking && tc == nil {
		return nil, nil
	}
	source, err := receipt.LocateSource(folder)
	if err != nil {
		return nil, nil
	}
	root, err := build.DefaultRoot()
	if err != nil {
		return nil, nil
	}
	parts := []string{command, source.Admitted(), warmOptions(opts)}
	if linking {
		stamp, ok := goStamp(tc.Go)
		if !ok {
			return nil, nil
		}
		// A CPU level --platform states (build.Toolchain.Level) chooses
		// the executable's instructions, so another level is another
		// request; the environment's GOAMD64 or GOARM64 is in WarmKey's
		// GO* variables already, and no level leaves the key as it was.
		target := tc.GOOS + "/" + tc.GOARCH
		if tc.Level != "" {
			target += "/" + tc.Level
		}
		if tc.PGO != "" {
			// A release build's profile is an input of every compile:
			// its bytes key the request (U4 PGO-RELEASE).
			data, err := os.ReadFile(tc.PGO)
			if err != nil {
				return nil, nil
			}
			target += " pgo=" + fmt.Sprintf("%x", sha256.Sum256(data))
		}
		parts = append(parts, stamp, tc.Root, target, tc.GOFLAGS, strings.Join(tc.Tags, ","), strconv.FormatBool(tc.GoBuild)+" "+strconv.FormatBool(tc.Strip)+" "+string(tc.Profile))
	}
	key := sketchfront.WarmKey(append(slices.Clone(parts), extra...)...)
	if key == "" {
		return nil, nil
	}
	w := &warm{cache: build.Cache{Root: root}, key: key, parts: parts}
	valid := func(a sketchfront.WarmAnswer) bool {
		if opts.command == "test" {
			return a.Test != nil
		}
		if !building {
			return a.Executable == "" && a.Test == nil
		}
		return a.Executable != "" && w.cache.HasExecutable(a.Executable)
	}
	return w, valid
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
	tc := actionFor(opts, selected)
	return &tc
}

// warmOptions are the options that change a build's, a check's or a
// test's answer.
func warmOptions(opts fileOptions) string {
	options := []string{opts.target, opts.platform.String(), strconv.FormatBool(opts.locked), strconv.FormatBool(opts.json), strconv.FormatBool(opts.debug),
		opts.seed, opts.evalSteps, opts.evalBytes, opts.states, opts.law, opts.skipLaw, opts.run, strconv.FormatBool(opts.native), strconv.FormatBool(opts.noViews), fallbackKey(opts), opts.rewrites, strconv.FormatBool(opts.goTrace), opts.phone.String(), strconv.FormatBool(opts.trace), fmt.Sprintf("%q", opts.more)}
	if opts.strict {
		// check --strict: its exit judges the answer; added only when set,
		// so the keys of every other request stay as they were.
		options = append(options, "strict")
	}
	return fmt.Sprintf("%q", options)
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

// recordAs stores the answer of a request whose normal path succeeded
// under the key of its parts and extra as they are now: a test run writes
// the law ledger it is keyed on, so the next run finds the ledger this one
// left.
func (w *warm) recordAs(answer sketchfront.WarmAnswer, extra ...string) {
	if w == nil || w.rec == nil {
		return
	}
	if key := sketchfront.WarmKey(append(slices.Clone(w.parts), extra...)...); key != "" {
		_ = w.rec.Record(w.cache, key, answer)
	}
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
