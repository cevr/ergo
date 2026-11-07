#!/bin/sh
# run.sh records one measurement set of the union-repr bench module into
# results/<UTC stamp>/. Run it from the module root, alone on the host
# (the wf10 runs hold the shared gate.lock), for example:
#
#   flock /home/exedev/.cache/baton/wf10/gate.lock \
#     systemd-run --user --wait --pipe --collect --same-dir -q \
#       -p MemoryMax=8G -p MemorySwapMax=0 --setenv=PATH="$PATH" --setenv=HOME="$HOME" \
#       env GOMEMLIMIT=6GiB GOCACHE=/tmp/opencode/m2/gocache GOMAXPROCS=4 sh run.sh
#
# COUNT (default 10) is the -count of the benchmark run. BENCH (default .)
# selects benchmarks.
set -eu
count=${COUNT:-10}
bench=${BENCH:-.}
stamp=$(date -u +%Y%m%dT%H%M%SZ)
out=results/$stamp
mkdir -p "$out"
tmp=$(mktemp -d)

load() { printf '%s %s\n' "$(date -u +%H:%M:%S)" "$(cat /proc/loadavg)" >>"$out/loads.txt"; }

{
	echo "stamp: $stamp"
	go version
	uname -srm
	echo "nproc: $(nproc)"
	echo "GOMAXPROCS: ${GOMAXPROCS:-unset}"
	grep -m1 'model name' /proc/cpuinfo || true
	echo "uptime before: $(uptime)"
} >"$out/env.txt"
load

# Contract tests and vet.
if { go vet ./... && go test -count=1 ./... ; } >"$out/tests.txt" 2>&1; then
	echo "tests exit: 0" >>"$out/tests.txt"
else
	echo "tests exit: $?" >>"$out/tests.txt"
	exit 1
fi

# Escape analysis of the forms: which boxes go to the heap.
go build -gcflags=-m -o /dev/null . 2>&1 | grep -E 'escapes to heap|does not escape' |
	grep -E '^\./(breaker|tidewalk|option|gc|clone|narrow)\.go' >"$out/escape.txt" || true

# Jump tables: the dense integer switches that Go lowers to a table.
go test -c -o "$tmp/bench.test" .
go tool objdump -s 'dispatch(U16D|U8D|P6x6Dx|P3x4Dx|U16A|P6x6A)$' "$tmp/bench.test" |
	grep -E '^TEXT|JMP [A-Z]+$|JMP \(|JMP [A-Z0-9]+\(' >"$out/jumptables.txt" || true

# Code size (question 3).
go build -o "$tmp/sections" ./tools/sections
{
	echo "step, describe, methods: text bytes of main.step, main.describe and the variants' methods (go tool nm -size)."
	echo "stripped: file bytes with -ldflags='-s -w'. Sections: bytes of the stripped binary's ELF sections."
	printf '%-6s %10s %6s %8s %8s  %s\n' form stripped step describe methods sections
	for f in a d e f aenum b; do
		go build -o "$tmp/$f" ./size/$f
		go build -ldflags='-s -w' -o "$tmp/$f.s" ./size/$f
		stripped=$(stat -c %s "$tmp/$f.s")
		go tool nm -size "$tmp/$f" >"$tmp/$f.nm"
		step=$(awk '$4=="main.step"{print $2}' "$tmp/$f.nm")
		describe=$(awk '$4=="main.describe"{print $2}' "$tmp/$f.nm")
		methods=$(awk '$3=="T" && $4 ~ /^main\.(\(\*)?V[0-9]+\)?\./ {s+=$2} END{print s+0}' "$tmp/$f.nm")
		printf '%-6s %10s %6s %8s %8s %s\n' "$f" "$stripped" "$step" "$describe" "$methods" "$("$tmp/sections" "$tmp/$f.s")"
	done
	echo
	echo "outputs (every form must print the same line as its pair):"
	for f in a d e f aenum b; do printf '%-6s %s\n' "$f" "$("$tmp/$f" x y)"; done
} >"$out/size.txt"
load

# The benchmarks.
echo "uptime before bench: $(uptime)" >>"$out/env.txt"
(while [ ! -e "$tmp/done" ]; do load; sleep 30; done) &
go test -run '^$' -bench "$bench" -benchmem -count "$count" -timeout 120m . >"$out/count$count.txt" 2>&1 || echo "bench exit: $?" >>"$out/env.txt"
: >"$tmp/done"
wait
echo "uptime after bench: $(uptime)" >>"$out/env.txt"
load

# GC trace of the churn benchmarks (question 4).
GODEBUG=gctrace=1 go test -run '^$' -bench 'GCChurn' -benchtime=3000000x -count 1 . >"$out/gctrace.txt" 2>&1 || true

if command -v benchstat >/dev/null 2>&1; then
	benchstat -col /form "$out/count$count.txt" >"$out/benchstat.txt" 2>&1 || true
elif [ -x "$HOME/go/bin/benchstat" ]; then
	"$HOME/go/bin/benchstat" -col /form "$out/count$count.txt" >"$out/benchstat.txt" 2>&1 || true
fi
echo "uptime end: $(uptime)" >>"$out/env.txt"
echo "$out"
