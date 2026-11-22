#!/usr/bin/env bash
# ENCDEC receipt: examples/wtcache's Set, its read-back rewritten by
# strconv.UnquoteInvertsQuote (-rewrites=on) vs kept (-rewrites=off) vs hand
# Go vs the floor (hand/main.go), counted as instructions:u at GOMAXPROCS=1.
#
#   bash benchmarks/laws/encdec/count.sh [CHECKOUT] [RUNS]
#
# CHECKOUT is the checkout to measure (default: the current directory); RUNS
# defaults to 5. Binaries and counts go to a fresh scratch folder ($TMPDIR).
# Needs perf on PATH (or PERF=<path>).
set -u
checkout=$(cd "${1:-.}" && pwd) || exit 2
runs=${2:-5}
perf=${PERF:-perf}
cd "$checkout" || exit 2
B=$(mktemp -d "${TMPDIR:-/tmp}/encdec-count.XXXXXX") || exit 2
echo "== checkout $checkout at $(git rev-parse --short HEAD 2>/dev/null), scratch $B"
nice -n 19 env GOMAXPROCS=4 go build -o "$B/ergo" ./cmd/ergo || exit 1
(cd benchmarks/laws/encdec/hand && nice -n 19 env GOMAXPROCS=4 go build -o "$B/hand" .) || exit 1
(cd examples/wtcache && "$B/ergo" inspect rewrites ./cmd/wtbench) || exit 1
nice -n 19 env GOMAXPROCS=4 "$B/ergo" build ./examples/wtcache/cmd/wtbench -o "$B/on" || exit 1
nice -n 19 env GOMAXPROCS=4 "$B/ergo" build ./examples/wtcache/cmd/wtbench -rewrites=off -o "$B/off" || exit 1
if cmp -s "$B/on" "$B/off"; then echo "== on and off are one binary"; exit 1; fi
run() { case $1 in hand|floor) echo "$B/hand $1" ;; *) echo "$B/$1" ;; esac; }
for w in on off hand floor; do
  echo "$w output: $(GOMAXPROCS=1 $(run $w))"
done
: > "$B/insns"
for r in $(seq "$runs"); do
  for w in on off hand floor; do
    c=$(GOMAXPROCS=1 nice -n 19 "$perf" stat -x, -e instructions:u $(run $w) 2>&1 > /dev/null | awk -F, '/instructions/ {print $1}')
    echo "$w $c" >> "$B/insns"
  done
done
for w in on off hand floor; do
  awk -v w=$w '$1==w {print $2}' "$B/insns" | sort -n | awk -v w=$w '{a[NR]=$1} END {m=a[int((NR+1)/2)]; printf "%s instructions:u: median %d min %d max %d spread %.2f%% (n=%d)\n", w, m, a[1], a[NR], 100*(a[NR]-a[1])/m, NR}'
done
echo "== count done"
