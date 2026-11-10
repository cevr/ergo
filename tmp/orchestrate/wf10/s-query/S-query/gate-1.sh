#!/usr/bin/env zsh
# S-query round gate: the focused checks, run from the worktree.
set -eu
umask 022
export GOMAXPROCS=2 GOMEMLIMIT=6GiB GOCACHE=/tmp/opencode/sq-gocache
echo 'format and diff'
test -z "$(gofmt -l internal/sketch/lower/std_http_test.go internal/sketch/lower/blocking.go runtime/v1/http.go)"
git diff --check 826a601a..HEAD
echo 'std http laws (QueryDecodesByTheRule) and HTTP lowering'
go test -p 2 -count=1 -timeout 15m -run 'TestStdHTTPLawsRun|TestHTTP' ./internal/sketch/lower
echo 'std admission and law ledger'
go test -p 2 -count=1 -timeout 15m ./internal/sketch/std
echo 'runtime http (Serve now reads Query through HTTPParseQuery)'
go test -p 2 -count=1 -timeout 15m -run 'HTTP' ./runtime/v1
echo 'vet touched packages'
go vet -p 2 ./internal/sketch/lower ./internal/sketch/std ./runtime/v1
echo 'ergo formatting of the std files'
go build -p 2 -o /tmp/opencode/sq-ergo ./cmd/ergo
/tmp/opencode/sq-ergo fmt --check internal/sketch/std/http/query.ergo internal/sketch/std/http/http.ergo
echo 'residue and gate cache coverage'
go test -p 2 -count=1 -timeout 15m ./internal/residue
go test -p 2 -count=1 -timeout 15m -run '^TestTestCommandsHaveCacheCoverage$' ./scripts/gate
echo 'gate exit 0'
