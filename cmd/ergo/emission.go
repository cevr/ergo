package main

import (
	"os"

	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketchfront"
)

// emissionRequest is the part of a build request of the loaded package l
// that decides the Go module a build of mode links: its mode, its source,
// the IR lowering, and for a run build the rewrite-law ledger it lowers
// with and whether a stale entry refuses. ergo build, ergo test and every
// --emitted reading (printEmitted, test --emitted --json) take it from
// here, so a digest names the module the build links: a reading of its
// own once hashed the text lowering where builds linked the IR one, and
// an edit of the IR lowering replayed the examples runner's old verdicts
// (gate-speed GS32c). The caller adds what only a build needs (output,
// cache, toolchain, reuse).
func emissionRequest(opts fileOptions, l loaded, mode build.Mode) sketchfront.BuildRequest {
	req := sketchfront.BuildRequest{Mode: mode, Source: l.source.Admitted(), IR: true}
	req.NoViews, req.Rewrites = opts.noViews, opts.rewrites
	if mode == build.ModeBuild {
		req.Ledger, req.Locked = ledgerPath(opts.file), opts.locked
		req.NoFallback = opts.noFallback || os.Getenv("ERGO_FALLBACK") == "off" // FALLBACK-OFF: a build only (main.go)
	}
	return req
}
