ready

Protocol: supplied implementor worker protocol (not protocol.md or safety.md).
Spec: `/home/exedev/.cache/baton/wf10/cli-units/brief-2.md`, C2 (S-term), round 1.
Head: `8f5899e4f0de64f101864f440501341cbde5a05d` (implementation; the commit that records this request follows it).
Base: `ec6f1be2` (CU5 head). Branch: `wf10/cu2`. Workspace: `/home/exedev/Developer/personal/.worktrees/ergo-v1b`.
Measured diff: `62 files changed, 1355 insertions(+), 150 deletions(-)` (`git diff --stat ec6f1be2..8f5899e4 | tail -1`).

## Changes

- `internal/sketch/std/term/{term,laws}.ergo` (new std package `term`): operation type `Terminal { SetMode(m Mode) fails NotTerminal, Restore(), ReadKey() Key fails io.ReadFailed, Size() Window fails NotTerminal }`; block forms `Raw` and `NoEcho` (generic over the block's T, E, R) that register `Restore` with `scope.OnExit`, so every exit path restores (value, failure, defect, stop). `Key` is a union (`Char`, `Return`, `Backspace`, `Tab`, `Escape`, arrows, `Interrupt`, `EndOfInput`, `Other`); `Decode(token)`; host provider `Host` (Ambient); test provider `Script` (plays keys, keeps modes in a `sync.Ref`). Laws: `RawRestores`, `NestedRestoresInOrder`, `ScriptPlaysKeys`, `DecodeNamesControls`.
- `internal/sketch/std/io/io.ergo`: `Stdin.IsTerminal()` and `Stderr.IsTerminal()`. One `IsTerminal` of `io.Host` cannot answer for both standard output and standard input, so the host Stdin provider is now `io.In` (`io.NewStdin()`, Ambient), and `Host` lost `ReadAll`/`Lines`: a stale `io.Stdin: io.NewHost()` is a check error, not a wrong answer. `Input` and `Errors` answer `IsTerminal`.
- `runtime/v1/host.go`: per-stream `IOStdinIsTerminal`, `IOStderrIsTerminal`; all three use isatty (termios read through `SyscallConn`, so the file keeps its blocking mode) instead of `ModeCharDevice` (`/dev/null` is a character device). The stdin `lineReader` is now one byte buffer with `take(stop, cut)`: lines, `ReadAll` and term keys read through it, so a key read after a line read loses no byte; a stop leaves the bytes in the buffer (replaces `unread`).
- `runtime/v1/term.go`, `term_unix.go`, `term_linux.go`, `term_darwin.go`: mode stack, raw/no-echo states, key splitting (`keyLen`) and normalisation (`keyToken`), window size. `runtime/embed.go`, `runtime/v1/files.go` (record table), `internal/sketch/lower/blocking.go` (bindings; `TermReadKey` waits on the caller's task).
- Migration: 33 example mains, `examples/jsonv` (test provider and main), 6 lower Go test programs and `testdata/flagprobe` bind `io.NewStdin()`.
- Tests: `internal/sketch/lower/std_term_test.go` (`TestTermModesRestore`, `TestTermLaws`), `pty_linux_test.go`, `pty_darwin_test.go`. Std ledgers and lists: `extern_callers_test.go`, `std_test.go`, the residue allowlist row of `runtime/embed.go` (internal/residue testdata), both mutant ledgers. Docs: `docs/spec/stdlib.md` (io rows/items 4–5, new `term` section, strengths row, four law rows).

## Run and evidence (measured)

Share D: GOMAXPROCS=2, -p 2, GOMEMLIMIT=6GiB, umask 022, one private GOCACHE (`/tmp/opencode/cu237/gocache`). Ad hoc checks ran in bounded scopes (common.md memory rule; it departs from the protocol's rule against worker scopes). One heavy check at a time; `ps` after each run showed no process of mine left.

Round gate: `sh /tmp/opencode/cu237/gate.sh cu2 ec6f1be2 '^TestTermModesRestore$' '<lower focus>' <32 programs>` in one bounded scope. Log: `/home/exedev/.cache/baton/wf10/cli-units/cu2/gate-1.log`. Exit lines: `ACCEPT_EXIT=0`, `GATE_EXIT=0`, `ROUND_GATE_EXIT=0`. Steps: gofmt, `ergo fmt --check` of changed .ergo, vet (runtime/v1, lower, std), full tests of std, residue, runtime/v1, runtime; cache coverage; lower focus `TestIO|TestStdin|TestTerm|TestStdLawsRun|TestStdHost|Blocking|TestFlagTree|TestAscending|TestStdFS`; `scripts/examples` on the 32 changed programs that have no tty/gui/net tag.

Acceptance at head: `go test -count=1 -run '^TestTermModesRestore$' ./internal/sketch/lower` → `--- PASS`, `ACCEPT_EXIT=0`. A built probe runs on a new pseudo-terminal (stdin = slave, stdout and stderr are pipes) for 24 seeds of generated raw / noecho / read / leave / stop sequences. At each read it checks the terminal's ICANON, ECHO and ISIG against the model, types a generated key (arrows in both forms, UTF-8, controls, an unnamed sequence) and checks the decoded name. A stop is `timeout.After` around a read that gets no key; its Expired leaves every scope by failure. After exit, the whole termios must equal the state before. It also checks `terminals 100` (stdin is a terminal; stdout and stderr are not). Replay: `ERGO_TERM_SEED=<n>`.
Red at base: `cu2/red.log`. The probe at ec6f1be2 fails its check with `unknown package "term"`. The brief's evidence `cli-pilots/evidence/check-red.txt` names `io.Stdin has no method IsTerminal`.

Mutants (`cli-units/cu2-mutants.log`, `MUTANTS_EXIT=0`): all 12 are killed. Raw restores only on success; NoEcho sets raw; Script restore drops the oldest mode; space decoded as control; host Restore restores the oldest; raw keeps ISIG; SS3 arrow not normalised; escape sequence split; rune split; stdin IsTerminal reads stdout; stderr IsTerminal reads stdin (all killed by TestTermModesRestore or TestTermLaws); the reader loses its buffered tail (killed by runtime/v1 TestIOLinesSplit). Each is in a ledger (lower with anchors, runtime/v1). New tests and the mutant only they kill: `TestTermModesRestore` is the only test that kills host-restore-oldest, raw-keeps-signals, ss3, escape-split, rune-split and both IsTerminal mutants. `TestTermLaws` is the only test that kills script-restore-drops-oldest.

## Intent, prior art and choices

Effect's `Terminal` exists to read keys from a person and display output. Ergo takes this: a key union matched exhaustively, and a mode that is a resource with a block form. It does not take the service tag (W4), the Effect value per read (W14) or the Scope value (W11). Go x/term does the same ioctls and leaves the restore to `defer`. OTP 26 raw mode is set back by the caller. Gleam has no terminal module. Roc basic-cli Tty has enable/disable with no pairing. Ergo lowers the block to one `OnExit` registration on the frame scope.

Choices (simpler/reversible): `Return`, not `Enter`, because `Enter` is a prelude name. Ops are `SetMode`/`Restore` and the size type is `Window`, to avoid clashing with provider function names. Key normalisation is in Go and naming is in Ergo `Decode`.

## Accepted decisions

- q1 accepted by the orchestrator in decisions.md: `unsafe` only in `runtime/v1/term_unix.go`'s `ioctl`. A constrained typed pointer is passed to it; the only conversion is inside its syscall expression. `tools/provenance/runtime-unsafe-allow.txt` records the one reviewed file/function, invariant and pty guard. No benchmark: no safe standard-library form exists (host syscall edge, no module dependencies).
- q2 accepted: keep the separate `io.NewStdin` host provider and migration. No open questions.

## Known gaps

- `examples/ledger` "test file" fails at base too (law `fs.Root.LeavingEscapes` for `Broken`, measured with a base build). It is left out of the examples step. Its other cases pass.
- Closure sweep of `internal/sketch/lower` not run. The new test reads only `/dev/ptmx` and `/dev/pts/N`, outside the module, and the sweep would run the whole lower suite.
- No darwin run here: the macOS check is the batch's. The darwin pty and ioctl code is written but was not run.
- `examples/tui` keeps its own key decoder. Moving it to std `term` is a follow-up.
- No speed claim. A mode change is one ioctl.
