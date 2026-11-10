#!/usr/bin/env zsh
set -eu
umask 022
export GOMAXPROCS=1 GOMEMLIMIT=6GiB GOCACHE=/tmp/opencode/e3b-gocache
unset ERGO_TS_TEST
echo 'format and diff'
test -z "$(gofmt -l internal/tsproj/http.go internal/tsproj/http_test.go internal/tsproj/http_accept_test.go internal/sketch/lower/std_http_test.go)"
git diff --check
echo 'mapper tests and stock HTTP acceptance'
ERGO_TS_TEST=1 go test -p 2 -count=1 -timeout 10m ./internal/tsproj
echo 'HTTP lowering laws and in-process typecheck'
go test -p 2 -count=1 -timeout 10m -run 'TestStdHTTPLawsRun|TestHTTP' ./internal/sketch/lower
echo 'std admission and law ledger'
go test -p 2 -count=1 -timeout 10m ./internal/sketch/std
echo 'total failure catch guard'
go test -p 2 -count=1 -timeout 10m -run '^TestJobsFailureNeedsAStatus$' ./scripts/examples
echo 'vet touched packages'
go vet -p 2 ./internal/tsproj ./internal/sketch/lower ./internal/sketch/std ./scripts/examples
echo 'example and std formatting, module laws and program cases'
go build -p 2 -o /tmp/opencode/e3b-ergo ./cmd/ergo
/tmp/opencode/e3b-ergo fmt --check internal/sketch/std/http/failure.ergo internal/sketch/std/http/router.ergo examples/jobs/...
/tmp/opencode/e3b-ergo test examples/jobs/...
go run -p 2 ./scripts/examples -ergo /tmp/opencode/e3b-ergo jobs
echo 'residue and gate cache coverage'
go test -p 2 -count=1 -timeout 10m ./internal/residue
go test -p 2 -count=1 -timeout 10m -run '^TestTestCommandsHaveCacheCoverage$' ./scripts/gate
echo 'closure sweep for mapper and example file readers (normal Go-only leg)'
ERGO_CLOSURE_SWEEP=internal/tsproj go test -p 2 -count=1 -timeout 10m -run '^TestEveryPackageRecordsOnlyItsClosure$' ./internal/closure
ERGO_CLOSURE_SWEEP=scripts/examples go test -p 2 -count=1 -timeout 10m -run '^TestEveryPackageRecordsOnlyItsClosure$' ./internal/closure
