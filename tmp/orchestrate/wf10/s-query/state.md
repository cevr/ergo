# S-query / S-target state

- Worktree /home/exedev/Developer/personal/.worktrees/ergo-l8b; wf10/s-query (base E3b 826a601a); wf10/s-target stacked on it.
- Share C: GOMAXPROCS=2 go test -p 2, nice 19, umask 022, scope MemoryMax=8G; GOCACHE /tmp/opencode/sq-gocache (remove at READY); CLI /tmp/opencode/sq-ergo; scratch /tmp/opencode/sq-scratch.

## Decisions
- S-query: std http `DecodeQuery[T wire]` over a Reader provider (derived codec; one rule, q39). Unknown keys skipped; missing -> WireMissingField unless json.Optional; scalar repeated -> QueryRepeated{Key}; slice takes all in order. ParseQuery = Serve's parser.
- q1 answered: S-target merges CU5 head ec6f1be2 into wf10/s-target (merge commit 23db15d5).
- S-target: webURL (WHATWG special-URL path/query) sent via url.URL.Opaque; authority still via url.Parse (origin binding unchanged); "//" path Go cannot carry -> Malformed (decided by: refuse, never send changed); exchange uses Transport.RoundTrip (Client.Do parsed Location and failed on %zz).

## Status
- S-query READY: code head ee762ffa, request commit 61cae863. gate-2.log exit 0; mutants-2.log 9/9.
- S-target READY: code head 88bd5f06, request commit follows. gate-1.log exit 0; mutants-2.log 22/22; red-1.log exit 1.

## Follow-ups
- Codec field default (absent list = empty) would need a language-wide rule.
- WHATWG: `http:x` / `http:///x` request URLs refused; HTTP_PROXY plain-http origin-form target; invalid-UTF-8 byte rule untested.
