ready
Protocol: supplied orchestrate worker protocol (implementor); /home/exedev/.cache/baton/wf10/common.md.
Spec: /home/exedev/.cache/baton/wf10/s-query/brief.md (unit S-target); gap G-L4-3 in /home/exedev/.cache/baton/wf10/cli-pilots/ergo-gaps.md.
Head: 88bd5f06 (code and tests; the request commit follows it on wf10/s-target).
Base: 61cae863 (S-query head, wf10/s-query), stacked. Branch wf10/s-target, never rebased or pushed.

## Branch shape (orchestrator answer to q1)

- 23db15d5 merges the CU5 head ec6f1be2 into S-query's branch, as a merge commit. This brings CU5's URL request (network.ergo, http_network.go) and the commits below it: CU1, the rpc/mutant re-anchors, and 8 commits since merge-base c590c1bb. The only conflicts were the lower and scripts/examples mutant ledgers. Both are lists of mutants, so both sides were kept.
- 3b5a2584 merges the later S-query fix (residue rows, request update) forward.
- S-target's own commits: cd8d9c8a (red test and table), 9718056a (feature), e527f9a1 (test fix and base targets), 88bd5f06 (residue rows).
- Diff stat 23db15d5..88bd5f06: 10 files changed, 1142 insertions(+), 16 deletions(-). This includes the S-query fix merge. The table is about 600 of those lines.

## What changed (by file)

- runtime/v1/http_network.go: `webURL`, an http(s) URL as the WHATWG URL Standard parses a special URL.
  - `webClean`: invalid UTF-8 bytes become U+FFFD; leading and trailing C0 controls and spaces are trimmed; tabs and newlines are removed.
  - `webPath`: "\" is "/"; dot segments in every `%2e` spelling are removed; a dot segment at the end leaves an empty last segment.
  - `webEncode` with the path set and the special-query set: C0 controls, DEL and non-ASCII bytes are percent-encoded. "%" and every byte outside the sets stay as given.
  - `resolve` for a Location: a scheme makes it absolute, a same-scheme `http:x` is relative, `//`, `/`, `?`, `#`, and path-relative.
  - `requestURL`: the target goes into `url.URL.Opaque`, which Go's `RequestURI` writes as it is.
  - Only the authority goes through `url.Parse` as before. So `networkOrigin`, the policy check, credential binding and the Host header are unchanged.
  - HTTPFetch uses webParse and resolve, where it used url.Parse and ResolveReference.
- runtime/v1/http_client.go:
  - `httpDoURL` makes the request for the origin, then sets `req.URL = u`, so an Opaque target survives. The fixed-origin HTTPDo passes the same URL as before.
  - `httpExchange` calls `Transport.RoundTrip`, not `Client.Do`. The property found that `Client.Do` parses a 3xx Location even with ErrUseLastResponse, and fails the whole exchange when `url.Parse` refuses that Location ("%zz"). A redirect is a response, so this was a bug for both Do and Fetch.
- runtime/v1/http_target_test.go (new): `TestHTTPFetchSendsTheWebTarget`. A raw loopback TCP listener reads the request line and the Host header of each request. Each row of the table is fetched (redirect rows with Follow{1}). The test compares `GET <target> HTTP/1.1` and Host with the recorded reference. A failure prints the row and the replay command. The listener exits on a failure; a quit channel prevents a hung test.
- runtime/v1/testdata/webtarget/gen.mjs and targets.jsonl:
  - A seeded generator over path, query and fragment tokens. The tokens include `%zz`, `%2F`, `%2e` spellings, `|`, `^`, `` ` ``, `{}`, quotes, `<>`, space, backslash, controls, DEL, tab/newline, non-ASCII and a lone surrogate. Leading and trailing junk, scheme case and separator forms (`//`, `\\`, `/\`) are also drawn.
  - There are 400 URL rows and 200 redirect rows: path-relative, absolute, backslash, query-only, fragment-only and same-scheme forms.
  - The table was recorded with `bun gen.mjs` (Bun 1.4.4 `URL`). Node 24 (ada) gives byte-identical output (measured: `cmp` of both outputs, exit 0).
- runtime/v1/testdata/mutants.json: 22 `st-*` mutants. cu5-caller-lost is re-anchored to the renamed `target` variable (its from-text had gone stale).
- docs/spec/stdlib.md: http item 11, "The request target": the rule, the two refusals and the proxy gap.
- internal/residue/testdata/wire-residue.txt: rows for the network sense of "wire" in the new test and generator.

## How to run (measured)

Share C: GOMAXPROCS=2, go -p 2, GOMEMLIMIT=6GiB, umask 022, nice 19, private GOCACHE /tmp/opencode/sq-gocache. Every run used `systemd-run --user --scope -p MemoryMax=8G -p MemorySwapMax=0`, per the departure in common.md:44.

- Acceptance: `go test -p 2 -count=1 -timeout 10m -run '^TestHTTPFetchSendsTheWebTarget$' ./runtime/v1` → `ok` (gate-1.log, line 3, `-run HTTP`).
- Red: S-target/red-1.log, exit 1, at cd8d9c8a (test only, CU5 runtime). Row 2 `HTTP://h\<%zz…` ends with `http.RequestFailed: invalid request URL`: Go's url.Parse refuses `%zz`. The pilot measured `|` re-escaped and `%2F` decoded on the same path.
- Round gate: `zsh S-target/gate-1.sh` from the worktree → S-target/gate-1.log at 88bd5f06, exit line `exit 0`. It runs:
  - gofmt and git diff --check;
  - runtime/v1 `-run HTTP` (the new property, CU5's TestHTTPNetworkMatchesModel and Stops, the fixed-origin client and server);
  - ./runtime (embed);
  - lower TestHTTPNetworkRuns|TestStdHTTPLawsRun|TestHTTP;
  - internal/sketch/std;
  - vet runtime/v1;
  - `ergo test examples/download/...` and `scripts/examples download`;
  - internal/residue, TestTestCommandsHaveCacheCoverage, and the closure sweep `ERGO_CLOSURE_SWEEP=runtime/v1`, because the new test reads testdata.
- Mutants: `PREFIX=st- bun S-target/judge.mjs` from the checkout → S-target/mutants-2.log, `exit 0`, 22 of 22 killed. Every kill is an assertion of TestHTTPFetchSendsTheWebTarget naming a row.
  - mutants-1.log was the first run. All were killed, but st-del hung until the test deadline. The client refused the DEL target after it had dialled, and the listener goroutine blocked forever on an unbuffered send. e527f9a1 fixed the listener: it skips an empty connection and has a quit channel.
- The S-query gate was also re-run at the S-query head (S-query/gate-2.log, exit 0).
- After each run, `ps -eo pid,pgid,etime,cmd` showed no process of mine left.

## Mutants (each caught by no existing test: no earlier test reads the request line; Go's net/http server decodes the target before a handler sees it)

st-percent-kept, st-del, st-path-set, st-query-set, st-query-uses-path-set, st-backslash, st-dotdot-spellings, st-dot-spelling, st-dotdot-end, st-dot-end, st-tab-kept, st-trim, st-fragment-sent, st-empty-query, st-reescaped, st-double-slash-sent, st-relative-keeps-last, st-query-only-location, st-same-scheme-absolute, st-fragment-location, st-backslash-authority, st-client-parses-location. Killer of each: runtime/v1:TestHTTPFetchSendsTheWebTarget.

## Intent and prior art (inferred)

The purpose: a program that holds a URL reaches the resource that URL names, which is what fetch and the WHATWG standard give a TypeScript program. Effect's `HttpClient` takes the URL through `new URL` and sends what fetch sends; Ergo takes that behaviour without the service tag. The policy, origin and credential rules of CU5 are unchanged.

- Go's `net/url` re-escapes through EscapedPath when RawPath is not "valid encoding", and refuses `%zz`. Go's documented way to send a target as is, `URL.Opaque`, is what the lowering uses.
- Gleam's httpc takes a `Request` with a path string and sends it through Erlang httpc, which does not re-normalise. Elixir's Req and Finch build the target from `URI`. `URI.parse` keeps the path as written, and Mint writes it as is.
- Neither of them runs the WHATWG normalisation. Ergo does, so a URL a TypeScript peer parses names the same bytes.

The hand-written Go control is the same Opaque assignment plus a WHATWG path parser. No speed claim is made, and no measurement was made.

## Known gaps

- Refused as Malformed (never sent changed):
  - a request URL whose scheme is not followed by two slashes (WHATWG accepts `http:x` and `http:///x`);
  - a path that starts with `//` and holds text that Go can carry only in absolute form. The table has such rows, and they check the refusal.
- Through an `HTTP_PROXY` for a plain-http URL, Go writes an Opaque target in origin form to the proxy. Not tested.
- The table cannot hold invalid UTF-8 bytes, because JSON strings cannot. The U+FFFD rule for bytes is stated but untested. A lone surrogate row reaches Go as U+FFFD.
- Scheme-relative and other-scheme Locations are parsed by webParse, but no table row draws a Location with a new authority.
- The fixed-origin `Do` keeps its decoded `Path`, which the client escapes (stdlib item 10), as the spec asks.
