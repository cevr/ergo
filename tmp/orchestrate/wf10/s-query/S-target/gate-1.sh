#!/usr/bin/env zsh
# S-target round gate: the focused checks, run from the worktree.
set -eu
umask 022
export GOMAXPROCS=2 GOMEMLIMIT=6GiB GOCACHE=/tmp/opencode/sq-gocache
echo 'format and diff'
test -z "$(gofmt -l runtime/v1/http_network.go runtime/v1/http_client.go runtime/v1/http_target_test.go)"
git diff --check 23db15d5..HEAD
echo 'runtime http: the web target property, the CU5 network model, the fixed-origin client'
go test -p 2 -count=1 -timeout 15m -run 'HTTP' ./runtime/v1
echo 'runtime embed'
go test -p 2 -count=1 -timeout 15m ./runtime
echo 'std http network and laws through the lowering'
go test -p 2 -count=1 -timeout 15m -run 'TestHTTPNetworkRuns|TestStdHTTPLawsRun|TestHTTP' ./internal/sketch/lower
echo 'std admission and law ledger'
go test -p 2 -count=1 -timeout 15m ./internal/sketch/std
echo 'vet touched packages'
go vet -p 2 ./runtime/v1
echo 'the download example (CU5 URL requests)'
go build -p 2 -o /tmp/opencode/sq-ergo ./cmd/ergo
/tmp/opencode/sq-ergo test examples/download/...
go run -p 2 ./scripts/examples -ergo /tmp/opencode/sq-ergo download
echo 'residue, gate cache coverage, closure sweep of runtime/v1 (the new test reads testdata)'
go test -p 2 -count=1 -timeout 15m ./internal/residue
go test -p 2 -count=1 -timeout 15m -run '^TestTestCommandsHaveCacheCoverage$' ./scripts/gate
ERGO_CLOSURE_SWEEP=runtime/v1 go test -p 2 -count=1 -timeout 30m -run '^TestEveryPackageRecordsOnlyItsClosure$' ./internal/closure
echo 'gate exit 0'
