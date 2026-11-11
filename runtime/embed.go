// Package runtimefiles (directory runtime) owns the one embed of the Go
// runtime that lowered Ergo programs link: the packages runtime/rt and
// runtime/v1. The lowering copies these files into every emitted module,
// beside the generated main package, so a program needs no module download
// and its build key covers these exact bytes. runtime/v1's Source reads
// from here; no other package embeds runtime sources for emission.
//
// Files returns the files of the packages an emitted module imports and of
// every runtime package they import, so the emitted tree builds alone.
//
// Prior art: Gleam ships prelude.mjs and ReScript its runtime modules
// beside generated code; Go's own toolchain embeds the sources it copies
// into a vendor or work tree as files, not as a download.
package runtimefiles

import (
	"embed"
	"fmt"
	"io/fs"
	"slices"
)

//go:embed rt/cause.go rt/clock.go rt/doc.go rt/gocontext.go rt/handle.go rt/idle.go rt/machine.go rt/regions.go rt/run.go rt/sched.go rt/sched_explore.go rt/sched_live.go rt/scope.go rt/task.go rt/tasks.go rt/timeout.go rt/trace.go rt/values.go
//go:embed v1/actor.go v1/actor_host.go v1/actor_trace.go v1/mailbox.go v1/trace.go
//go:embed rt/rttrace/encode.go rt/rttrace/jsonl.go rt/rttrace/rttrace.go
//go:embed v1/context.go v1/data.go v1/exit.go v1/explore_laws.go v1/explore_off.go v1/explore_on.go v1/queue.go v1/runtime.go v1/task.go v1/text.go
//go:embed v1/host.go v1/term.go v1/term_unix.go v1/term_linux.go v1/term_darwin.go
//go:embed v1/http.go v1/http_client.go v1/http_network.go
//go:embed v1/embed.go
//go:embed v1/atomic.go
//go:embed v1/collections.go
//go:embed v1/time.go v1/timetext.go v1/wire.go v1/json.go v1/toml.go v1/rpc.go
//go:embed v1/rand.go
//go:embed v1/sync.go v1/set.go v1/script.go v1/shrink.go
//go:embed v1/path.go v1/numbers.go v1/digest.go v1/goerror.go v1/gocall.go v1/gocallback.go v1/csv.go
//go:embed v1/exec.go v1/exec_exit_kqueue.go v1/exec_exit_linux.go v1/exec_start.go
//go:embed explore/explore.go
//go:embed testgen/stream.go
//go:embed v1/moves.go v1/static.go
//go:embed v1/redacted.go v1/redacted_text.go v1/config_names.go v1/toml_encode.go
//go:embed explore/sched/dfs.go explore/sched/pct.go explore/sched/sched.go explore/sched/token.go explore/sched/worker.go
var sources embed.FS

// Module is the path of the runtime's Go module, which an emitted module
// requires and replaces with the copied tree.
const Module = "ergo.local/prototype"

// pkg is one runtime package: its directory under runtime/, the files of it
// that an emitted module holds, and the runtime packages it imports.
type pkg struct {
	files   []string
	imports []string
}

// packages are the runtime packages, by import path relative to Module.
// The list of files is every non-test file of the directory except those
// that serve only the compiler (runtime/v1/files.go); a test keeps both
// lists and the imports equal to the directories.
var packages = map[string]pkg{
	"runtime/testgen": {files: []string{"stream.go"}},
	"runtime/explore": {
		files: []string{"explore.go"},
	},
	"runtime/rt": {
		files: []string{"cause.go", "clock.go", "doc.go", "gocontext.go", "handle.go", "idle.go", "machine.go", "regions.go", "run.go", "sched.go", "sched_explore.go", "sched_live.go", "scope.go", "task.go", "tasks.go", "timeout.go", "trace.go", "values.go"},
	},
	"runtime/v1": {
		files:   []string{"actor.go", "actor_host.go", "actor_trace.go", "atomic.go", "collections.go", "config_names.go", "context.go", "csv.go", "data.go", "digest.go", "embed.go", "exec.go", "exec_exit_kqueue.go", "exec_exit_linux.go", "exec_start.go", "exit.go", "explore_laws.go", "explore_off.go", "explore_on.go", "gocall.go", "gocallback.go", "goerror.go", "host.go", "http.go", "http_client.go", "http_network.go", "json.go", "mailbox.go", "moves.go", "numbers.go", "path.go", "queue.go", "rand.go", "redacted.go", "redacted_text.go", "rpc.go", "runtime.go", "script.go", "set.go", "shrink.go", "static.go", "sync.go", "task.go", "term.go", "term_darwin.go", "term_linux.go", "term_unix.go", "text.go", "time.go", "timetext.go", "toml.go", "toml_encode.go", "trace.go", "wire.go"},
		imports: []string{"runtime/explore/sched", "runtime/rt", "runtime/rt/rttrace"},
	},
	// The schedule explorer's strategies and worker transport: the
	// explored build's runtime/v1 imports it (explore_on.go).
	"runtime/explore/sched": {
		files: []string{"dfs.go", "pct.go", "sched.go", "token.go", "worker.go"},
	},
	"runtime/rt/rttrace": {
		files:   []string{"encode.go", "jsonl.go", "rttrace.go"},
		imports: []string{"runtime/rt"},
	},
}

// File is one runtime source file, at its slash-separated path relative to
// the runtime module's root, such as runtime/rt/task.go.
type File struct {
	Path string
	Data []byte
}

// Files returns the files of each named runtime package (an import path
// relative to Module, such as runtime/v1) and of every runtime package it
// imports, transitively, sorted by path. Each Data is a fresh copy.
func Files(imports ...string) ([]File, error) {
	need := map[string]bool{}
	var visit func(string) error
	visit = func(path string) error {
		if need[path] {
			return nil
		}
		p, ok := packages[path]
		if !ok {
			return fmt.Errorf("runtimefiles: %s is not a runtime package", path)
		}
		need[path] = true
		for _, dep := range p.imports {
			if err := visit(dep); err != nil {
				return err
			}
		}
		return nil
	}
	for _, path := range imports {
		if err := visit(path); err != nil {
			return nil, err
		}
	}
	var files []File
	for path := range need {
		for _, name := range packages[path].files {
			data, err := sources.ReadFile(path[len("runtime/"):] + "/" + name)
			if err != nil {
				return nil, fmt.Errorf("runtimefiles: %s/%s is not embedded: %w", path, name, err)
			}
			files = append(files, File{Path: path + "/" + name, Data: data})
		}
	}
	slices.SortFunc(files, func(a, b File) int {
		if a.Path < b.Path {
			return -1
		}
		if a.Path > b.Path {
			return 1
		}
		return 0
	})
	return files, nil
}

// Package returns the embedded files of one runtime package directory, such
// as "v1", as a file system rooted at that directory.
func Package(dir string) fs.FS {
	sub, err := fs.Sub(sources, dir)
	if err != nil {
		panic("runtimefiles: " + err.Error())
	}
	return sub
}
