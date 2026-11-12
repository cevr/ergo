# Ports of the IR compiler patch

The IR build path compiles Ergo bodies with a patched `cmd/compile` (plan-tier §1.4). `go1.27.0.patch` is the patch, against the pristine Go release named in `lock`. Each Go release ergo pins gets one row here, written by its port (plan-tier §1.5, PORT-<ver>): at rc1 for a minor release, at once for a patch release.

Run a port from the repository root, with a pristine Go installation that holds its source (a `golang.org/toolchain` module copy is one; it is read only):

```
go run ./scripts/irport -goroot DIR -work NEW_DIR -noder
```

`irport` checks the patch against `lock`, copies the Go installation into `NEW_DIR`, applies the patch with ergo's own applier (`toolchain.ApplyPatch`; a rejected hunk is a conflict), builds the patched `cmd/compile`, runs the noder tests, and checks **stock equivalence**: without `-ergoir`, the patched compiler builds every example binary byte for byte as the pristine one does. It prints the row. Then run the agreement tests with the ported compiler (`ERGO_IRSPIKE_GOROOT_IR=NEW_DIR/goroot`, `ERGO_IRSPIKE_GOROOT_SOURCE=DIR`):

```
go test -count=1 -run 'TestIRMatchesSource|TestIRAgreesOnExamples|TestIRRuntimeChecks|TestIRBodyThatDoesNotCheck' ./internal/experimental/irspike
```

A port that passes adds the line `port <release> <patch>` to `lock`: ergo builds only with a release that has one (`internal/toolchain`, Patch). A failed port pins ergo to the last ported release; a failed security-release port goes to the owner at once.

| Go | conflicts | patch lines | files | export format | noder tests | stock equivalence | agreement tests |
|---|---|---:|---|---|---|---|---|
| go1.27.0 (2026-11-12, IRT0a) | 0 of 7 hunks rejected | 782 | 7: base/flag.go, noder/ergoir.go, noder/ergoir_call.go, noder/ergoir_expr.go, noder/ergoir_stmt.go, noder/ergoir_types.go, noder/unified.go | pkgbits V4 | ok | 54 of 54 examples | all pass (the IRT0a round gate) |
| go1.27.3 (2026-11-12, IRT0a) | 0 of 7 hunks rejected | 782 | the same 7 | pkgbits V5 | ok | 54 of 54 examples | TestIRMatchesSource, TestIRRuntimeChecks, TestIRBodyThatDoesNotCheck pass; TestIRAgreesOnExamples not run |
| go1.27.2 (2026-11-12, IRT0b; the Mac check's system Go, linux/amd64 source) | 0 of 10 hunks rejected | 1960 | the same 10 | pkgbits V5 | ok | 55 of 55 examples | not run |
| go1.27.4 (2026-11-12, IRT0b; the pin) | 0 of 10 hunks rejected | 1960 | 10: base/flag.go, noder/ergoir.go, noder/ergoir_call.go, noder/ergoir_expr.go, noder/ergoir_fail.go, noder/ergoir_stmt.go, noder/ergoir_text.go, noder/ergoir_text_shape.go, noder/ergoir_types.go, noder/unified.go | pkgbits V5 | ok | 55 of 55 examples | not run |
