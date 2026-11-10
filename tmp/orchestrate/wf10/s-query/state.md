# S-query / S-target state

- Worktree /home/exedev/Developer/personal/.worktrees/ergo-l8b; branch wf10/s-query at E3b 826a601a; wf10/s-target stacked on it.
- Share C: GOMAXPROCS=2 go test -p 2, nice 19, umask 022, scope MemoryMax=8G; GOCACHE /tmp/opencode/sq-gocache (remove at READY); CLI /tmp/opencode/sq-ergo; scratch /tmp/opencode/sq-scratch.

## Decisions
- S-query: std http `DecodeQuery[T wire]` over a Reader provider (derived codec; one rule, q39). Unknown keys skipped; missing -> WireMissingField unless json.Optional; scalar repeated -> QueryRepeated{Key}; slice takes all in order.
- ParseQuery(raw) extern HTTPParseQuery = the parser Serve uses (Serve switched to it; same Go call).
- TS mapper: routes carry no query (tsproj/http.go:346) -> no change. jobs example reads no query -> no change.
- q1 answered: S-target merges CU5 head ec6f1be2 into wf10/s-target (merge commit), fixes http_network.go there.

## S-query: READY candidate
- commits: 8d15613f red probe, 212f51b1 feature, 37fc403c residue rows; request commit after.
- gate-1.log exit 0; mutants-2.log 9/9 killed; red-1.log exit 1.

## Next
- S-target: branch wf10/s-target from s-query head, merge ec6f1be2, red property (wire bytes vs WHATWG table), fix.

## Follow-ups
- Codec field default (absent list = empty) would need a language-wide rule.
