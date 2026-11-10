ready

Protocol: implementor worker protocol supplied in the worker prompt (Implementor and Rounds); protocol.md and safety.md were not read.
Spec: /home/exedev/.cache/baton/wf10/g1-http/brief.md ("Added 2026-11-10" static files) with decisions.md's last entry (G1f confinement, option 1). There is no separate spec.md.
Head: ad43f0d7effd6e945c4f1f55a28fe7b2f113212a

# G1f round 1: confined fs.Root, then http.Files over it

- Branch `wf10/g1f`, worktree /home/exedev/Developer/personal/.worktrees/ergo-g1, stacked on `wf10/g1`.
- Base `b0108c96c238ee7dffcbc9f9a9dfc337da62eddf` (G1 core READY, its request already committed); head ad43f0d7effd6e945c4f1f55a28fe7b2f113212a.
- `git diff --stat b0108c96..HEAD | tail -1`:  14 files changed, 1236 insertions(+), 3 deletions(-)

## What changed, by file

- `internal/sketch/std/fs/fs.ergo`: `type Escapes {Path}`; open operation type `fs.Root {ReadFile, ReadDir, Stat}`, each `fails NotFound | Denied | Failed | Escapes`, with member law `LeavingEscapes` (a path starting with ".." or "/" is Escapes for every operation, whatever the tree); sealed `fs.HostRoot` (runtime type, unexported `hostRooted()`, law-exempt with reason); `extern fs.OpenRoot(dir) HostRoot fails NotFound | Denied | Failed needs Ambient`.
- `runtime/v1/host.go`: `Escapes`, `*HostRoot{dir}`, `FSOpenRoot`. Each operation calls `os.OpenRoot(dir)`, resolves with `root.ReadFile` / `root.Open`+`ReadDir` / `root.Stat`, and closes; no descriptor kept, no canonicalize-then-read. `path escapes from parent` (os's errPathEscapes) maps to Escapes, the rest as `fsErr`. Methods take the caller's `*rt.Task` and are `//ergo:host`.
- `runtime/v1/files.go`: std record row `fs.Escapes`.
- `internal/sketch/lower/blocking.go`: bindings `FSOpenRoot {}`, `HostRoot.ReadFile/ReadDir/Stat {task: true}` (no wait, no release).
- `internal/sketch/std/fstest/tree.ergo`: `fstest.Tree`, `Node = File | Dir | Link`, `NewTree`; resolution written after Go's `os/root_openat.go doInRoot` (".." restart and escape on count > i, link splicing with joined-"." rule, absolute target escapes, 8 links, trailing "/" on a file is "not a directory", Stat Name = filepath.Base of the given path).
- `internal/sketch/std/fstest/recorder.ergo`: `fstest.Recorder` / `NewRecorder(inner)` / `Paths`: a Root that keeps every path it is asked for.
- `internal/sketch/std/http/files.ergo`: `http.Files(root fs.Root, prefix string, r Request) Response` and `http.ContentType(name)`. Lexical name (prefix, drop ""/"." elements), 405 + `Allow: GET, HEAD` for other methods, one 403/404 rule (403: a ".." element, which reaches no read, or Denied/Escapes from the root; 404: NotFound, Failed, no element, no prefix), no directory listing, empty file without Content-Type, HEAD through Serve.
- `internal/sketch/std/http/laws.ergo`: law `FilesAsksBeneath(steps []int, prefixed bool)`: via `fstest.Recorder`, Files asks the root at most one path, relative, with no ""/"."/".." element, and none for a ".." or unprefixed request.
- `docs/spec/stdlib.md`: http rule 8 + prior art (Wisp serve_static is lexical-only, source inspected; Go FileServer/os.Root; Plug.Static by docs; ReScript binds host); fs table rows and rules 20–23; ledger rows `fs Root.LeavingEscapes`, `http FilesAsksBeneath`.
- Tests: `internal/sketch/lower/std_fs_root_test.go` (`TestFSRootTreeAgreesWithHost`, program `fsRoot` registered in `table_programs_test.go`); `std_http_test.go` probe adds `HttpFilesAsksBeneath` (law restated) and `FilesAnswersByTheRule` (Files vs a reference of the rule over a Tree with links out/in/loop, every name each case); `internal/sketch/std/extern_callers_test.go` inventory adds `fs.OpenRoot`.
- Mutant ledger `internal/sketch/lower/testdata/mutants.json`: 9 `root-*` / `files-*` rows.

## Tests and the mutants they catch (AGENTS.md "Tests")

- `TestFSRootTreeAgreesWithHost` (seeded model-based differential, full tier; builds Go because the reference is os.Root on a real directory and Tree is Ergo with no interpreter): 120 trees × 40 ops; Linux every line equal (value, failure kind, path, reason); no line of either provider holds the outside secret; LeavingEscapes held ≥ 20 times on both; outcome list incl. "link Escapes" (Escapes without ".." or leading "/"). `ERGO_FS_SEED=n` replays; failure prints nodes, ops, both outputs. Kills `root-host-joins`, `root-host-escape-failed`, `root-tree-absolute-target`, `root-tree-dotdot-past-root`.
- `TestStdHTTPLawsRun` probe: kills `files-dotdot-read`, `files-prefix-ignored`, `files-escape-404`, `files-empty-content-type`, `files-extension-case`.
- Each mutant applied alone and restored by `/tmp/opencode/g1f/mutate.ts`; logs `/home/exedev/.cache/baton/wf10/g1-http/G1f/mutant-<name>.log`; all 9 killed (two first survived; the property now checks every name each case, then both were killed).

## How to run

```
cd /home/exedev/Developer/personal/.worktrees/ergo-g1
GOMAXPROCS=1 go test -p 2 -count=1 -run '^(TestFSRootTreeAgreesWithHost|TestStdHTTPLawsRun)$' ./internal/sketch/lower/
ERGO_FS_SEED=7 GOMAXPROCS=1 go test -p 2 -run TestFSRootTreeAgreesWithHost ./internal/sketch/lower/
```

## Gate

- Log `/home/exedev/.cache/baton/wf10/g1-http/G1f/gate-1.log`; exit line: `EXIT 0` (all steps ok)
- Gate = `-short` tests of std, lower, runtime/v1, residue; full-tier targeted tests (acceptance below, TestHTTPHandlerRows, TestFSMemoryAgreesWithHost); `-race` HTTP/Host runtime tests; vet; gofmt; ergo fmt --check of the changed .ergo files.

## Acceptance check

`GOMAXPROCS=1 go test -p 2 -count=1 -run '^(TestFSRootTreeAgreesWithHost|TestStdHTTPLawsRun)$' ./internal/sketch/lower/` at the head: `EXIT 0`, log /home/exedev/.cache/baton/wf10/g1-http/G1f/accept-1.log

## Decisions (state.md has the "decided by" lines)

- Host provider is a sealed runtime type, not an fs record: a second `ReadFile` on a record in `fs` would make existing plain calls `fs.ReadFile(h, p)` ambiguous (types.md Methods 3). Its Go methods take the task, so `*v1.HostRoot` already is the program's lowered `fs.Root` interface; no compiler adapter was added.
- Per-call `os.OpenRoot(dir)`: no descriptor is kept (fs's "no file stays open" rule); costs one open/close of the directory per read.
- Content types by an if chain (no per-call map allocation; a std package `let` has no Go name today).

## Known gaps

- `HostRoot` holds `dir` by name: a rename or replacement of `dir` itself between calls changes the root (the root is the directory at `dir` when each call starts). Links inside are confined.
- On non-Linux hosts the agreement test checks the no-secret property and the law, not line equality (failure reasons differ).
- Files buffers the whole file (G1 bodies are buffered); no Range, ETag or index.html. No benchmark was run for Files.
- `http` imports `fstest` for its law file (the law observes paths through `fstest.Recorder`); a later split of laws into a test-only build would drop that edge.
- rpc does not use this yet (later unit, as the brief says). N1 not started.
