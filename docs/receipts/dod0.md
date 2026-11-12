# DOD0 design receipt: ids, columns, stored-id bounds and a VT step, in hand Go

This receipt measures four data-oriented forms in hand Go, with no Ergo, so that the `dense` design (DOD track) fixes its choices on numbers and not on guesses. Its numbers decide design priorities. They are not a claim about Ergo's speed: each Ergo claim waits for its own three-sided receipt (Ergo, hand Go, base).

Labels: every number below is **measured** on the host below. A ratio from `opcount` is the median of the paired ratios, with no verdict; a verdict word (`less`, `underpowered`) is strobe's. No test pins a number of this file.

## Host and method

| Item | Value |
| --- | --- |
| CPU | AMD EPYC 9554P 64-Core Processor, 16 logical CPUs (exe workbox KVM guest) |
| OS | Ubuntu 24.04.4 LTS, Linux 6.12.115 |
| Toolchain | go version go1.27.0 linux/amd64 |
| Sides | `GOMAXPROCS=1`, nice 19, one systemd scope with MemoryMax=8G and no swap |
| Load average | 47 to 75 on 16 CPUs for every run (other lanes and gates shared the host) |
| Date | 2026-11-12, 22:38Z to 23:12Z |
| Programs | `docs/receipts/dod0/` at commit 13ecc5d8e, a Go module of its own (`ergo.local/receipts/dod0`), so the compiler's module never builds them |

Two tools give the numbers:

1. **Counted** (`scripts/opcount` at 4c8b04d19, command mode): each round runs both sides as fresh processes in alternating order (A B, then B A), 6 rounds, and counts the user instructions of each process with the CPU's counter. It also gives CPU seconds and the peak resident set (VmHWM). Instruction counts do not change with load; CPU seconds do.
2. **Paired verdicts** (strobe 0.32.0 through the STROBE bridge, `strobe/bridge/run.sh`, pinned by version and npm integrity; `--pin none --reserve-cpus off --power keep`): `cpu.work` of the process tree, base and head in alternating pairs, a confidence interval, and a verdict or "underpowered". On this loaded host a timed effect under about 10% is underpowered, which is no claim.

Each program prints the same line on both sides (the sides compute the same result; checked before every run) and writes the runtime's counters to stderr at its end: `/gc/scan/heap:bytes`, `/gc/cycles/total:gc-cycles`, `/gc/heap/allocs:bytes`, the GC and user CPU classes, and the live heap. The scratch files of the run (opcount JSON, strobe JSON) are in the run directory `wf10/dod/dod0/receipts/`; they are not in the repository.

Build the sides from `docs/receipts/dod0`:

```sh
go build -ldflags=-X=main.side=64  -o idwidth64  ./idwidth
go build -ldflags=-X=main.side=32  -o idwidth32  ./idwidth
go build -ldflags=-X=main.side=u32 -o idwidthu32 ./idwidth
go build -ldflags=-X=main.side=aos -o layout-aos ./layout
go build -ldflags=-X=main.side=soa -o layout-soa ./layout
go build                -o bounds-checked   ./bounds
go build -gcflags=-B    -o bounds-unchecked ./bounds
go build -ldflags=-X=main.side=switch -o vt-switch ./vt
go build -ldflags=-X=main.side=table  -o vt-table  ./vt
```

Counted runs (from the repository root at 4c8b04d19, or a `git archive` of it), with `GOMAXPROCS=1` in the environment:

```sh
go run ./scripts/opcount -cmd -rounds 6 -memory 4GiB -base idwidth64 -head idwidthu32 -- 4000000 3
```

and so on for each row below, with the base, head and arguments in its table. Paired verdicts:

```sh
bash strobe/bridge/run.sh strobe run --base "env GOMAXPROCS=1 idwidth64 4000000 3" --head "env GOMAXPROCS=1 idwidthu32 4000000 3" --primary cpu.work --runs 1 --max-time 12m --json
```

## Row 1: a tree walk with 4-byte against 8-byte ids

`idwidth NODES WALKS`: one slice of nodes `{first, next, parent id; val int32}`, each node linked under a random earlier node, walked depth first from the root through `first` and `next`, and up the parent chain from every 64th node. The node is 16 bytes with 4-byte ids and 32 bytes with 8-byte ids. Random parents make the walk read nodes in no order.

| Measure | 8-byte (`int64`) | 4-byte (`uint32`) | 4-byte (`int32`) |
| --- | --- | --- | --- |
| node bytes | 32 | 16 | 16 |
| tree bytes, 4,000,000 nodes | 128,000,000 | 64,000,000 | 64,000,000 |
| peak RSS, 4,000,000 nodes × 3 walks | 127,588 KiB | 64,996 KiB (×0.509) | 65,064 KiB (×0.510) |
| user instructions, 4,000,000 × 3 | 648,374,812 | 639,978,525 (×0.987) | 678,739,180 (×1.047) |
| user instructions, 50,000 × 240 (in cache) | 357,187,590 | 357,051,597 (×1.000) | 395,016,696 (×1.106) |
| `cpu.work`, 4,000,000 × 3 (strobe, 12 pairs) | 2,575,457 µs | 2,224,914 µs: **−14% [−24% … −4%], less** | — |
| CPU seconds, 4,000,000 × 3 (opcount, 6 rounds) | 2.99 | 2.45 (×0.813) | 2.32 (×0.784) |

The signed 4-byte id costs a sign extension at each load that becomes an index (+4.7% instructions out of cache, +10.6% in cache); the unsigned one costs nothing, since a 32-bit load clears the upper half. The `int64` and `int32` counts are from the first run, the `uint32` counts from the second; the `int64` count moved by 0.003% between the two.

## Row 2: one slice of records with a string field against one slice per field

`layout ROWS ROUNDS`: 2,000,000 rows `{name string; x, y, z int64; flags uint32}` (48 bytes), as one slice of records (AoS) or as five column slices (SoA). Each round reads the numeric fields of every row and the name of every 16th row, then makes 16 MiB of pointer-free garbage, so the collector runs while the table is live. The table stays live to the end.

| Measure | AoS | SoA |
| --- | --- | --- |
| scannable heap (`/gc/scan/heap:bytes`, table live) | 96,031,528 | 32,051,256 (−67%) |
| user instructions, 30 rounds, default GC | 4,651,654,561 | 5,301,881,542 (×1.140) |
| user instructions, 30 rounds, `GOGC=off` | 2,918,494,986 | 3,638,160,465 (×1.247) |
| GC share of instructions ((default − off) / default) | 37.3% | 31.4% |
| GC instructions (default − off) | 1,733,159,575 | 1,663,721,078 (−4.0%) |
| runtime GC CPU share (one run, 30 rounds) | 22.8% | 21.0% |
| GC cycles (one run, 30 rounds) | 6 | 7 |
| `cpu.work`, 6 rounds (strobe, 12 pairs) | 473,908 µs | 476,716 µs: −3% [−9% … +2%], underpowered (~118 pairs needed) |
| CPU seconds, 30 rounds (opcount) | 0.97 | 0.99 (×0.979) |

The columns scan a third of the bytes, but here the collector's instructions go mostly to the garbage, so GC instructions fall only 4%. The hand columns cost 25% more mutator instructions: Go checks each column's index on its own, five compares where AoS has one. CPU shows no difference that this host can see.

## Row 3: stored-id reads with and without the bounds compare

`bounds ROWS ROUNDS`, one source built twice: as is, and with `-gcflags=-B`, which removes every bounds check (the upper bound of what a fact can remove). A gather (`values[ref[i]]` for every row, the ids stored in `ref`) and a chase (`at = next[at]` through one cycle of every row).

| Measure | checked | `-B` |
| --- | --- | --- |
| user instructions, 4,000,000 rows × 5 | 728,676,611 | 596,589,475 (×0.819) |
| user instructions, 100,000 rows × 200 (in cache) | 370,835,823 | 250,533,675 (×0.676) |
| `cpu.work`, 4,000,000 × 5 (strobe, 12 pairs) | 4,400,374 µs | 4,451,836 µs: +1% [−9% … +6%], underpowered (~145 pairs needed) |
| CPU seconds, 4,000,000 × 5 (opcount) | 4.56 | 4.83 (×1.023) |
| CPU seconds, 100,000 × 200 (opcount) | 0.28 | 0.29 (×0.978) |

The compare is 18% of the instructions when the reads miss the cache and 32% when they hit it. Out of cache the loads wait for memory, and the compare runs in their shadow: no CPU difference is visible. In cache this host's timing is too noisy to show the instruction cut (spread over 40%).

## Row 4: a VT parser step as a table against a switch

`vt MIB ROUNDS`: the parser of Paul Williams' DEC state machine for the ground, escape and CSI states, with OSC strings (8 states, as in Ghostty's `Parser.zig`), over 16 MiB of generated terminal output (text, SGR and cursor sequences, OSC titles, control and stray bytes), 4 rounds. The switch side calls `step(state, byte)`. The table side reads `table[state][byte]`, and the table is the switch evaluated over every state and every byte at start-up: the form a table lowering of a machine (MT1) would emit. Both sides run the same actions and print the same counts.

| Measure | switch | table |
| --- | --- | --- |
| user instructions | 3,599,453,947 | 2,699,904,218 (×0.750) |
| `cpu.work` (strobe, 12 pairs) | 518,704 µs | 427,450 µs: **−18% [−23% … −8%], less** |
| CPU seconds (opcount) | 0.60 | 0.49 (×0.873) |
| peak RSS | 18,984 KiB | 18,984 KiB |

## Decisions

1. **Dense ids are 4 bytes and unsigned** (row 1). A `dense.ID` holds a `uint32`, and `Opt`'s "none" is all bits set. The 4-byte id halves the tree's memory and its resident set and cuts `cpu.work` 14% (decided) on a tree that outgrows the cache. A signed 4-byte id pays 5% to 11% more instructions for its sign extensions; an unsigned one pays nothing. A store holds at most 2^32 − 1 rows; `Put` past that is a defect. DN1 takes this. Reverse by a receipt that shows the limit binds a corpus program; then `ID` holds an `int`.
2. **SOA1 stays a candidate, behind LAY1** (row 2). Columns cut the scannable heap by two thirds, but this receipt sees no CPU gain and only 4% fewer GC instructions, and hand columns in Go pay one bounds compare per column. Hand columns stay the v1 form. If SOA1 is built, its lowering must check one length for all columns of a row (a fact over the store), or it costs more instructions than the records. Reverse by a corpus receipt where the collector's scan, not garbage, is the GC cost.
3. **INV1 stays a candidate; stored-id facts are an in-cache gain** (row 3). The bounds compare is 18% to 32% of the instructions of a stored-id read loop, and no CPU difference is visible out of cache. So DN3's facts (`given Has`, `keeps Has`) are worth their cost for hot loops over data that fits the cache, and the one predicted compare that C3 keeps elsewhere is cheap. INV1 removes test-build checks, not this run-build compare: it waits for DN3's test-build receipt, as C4 says. Reverse by a CPU verdict on a quiet host that shows the compare out of cache.
4. **MT1 has a measured gain over a switch** (row 4). A table step is 25% fewer instructions and 18% less `cpu.work` (decided) than the switch for the same parser. MT1 stays gated on DT3 (an Ergo machine above 1.0× of the hand table after JT1), and this row tells DT3 what to expect: a `match` lowering that equals a Go switch is about 1.2× of the table. Reverse by DT3 showing JT1's lowering at the table's speed.

*Prior art:* Zig's compiler and Yuku use `u32` ids (`enum(u32)` with `maxInt(u32)` as none), Go's `ssa.ID` is `int32`, and cranelift's entity keys are `u32` newtypes; this row's sign-extension cost is why the unsigned form is the right one in Go. Odin's `#soa` and Zig's `MultiArrayList` make columns a declared layout; Andrew Kelley's data-oriented design talk names struct-of-arrays as a padding and cache measure, which row 2 does not contradict (the scan cut is real) but does not confirm as a CPU gain on this workload. Ghostty's parser and Paul Williams' state machine run the step as a table built at compile time.
