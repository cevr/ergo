# Std count: Ergo's std against Go's, in counted instructions and allocations

Written by `go run ./scripts/stdcount` (std-better SB0). Do not edit by hand: run the harness again.

Measured head: `f42423fa125bdf455f0b296e77351a16ed2da873` (clean), 2026-11-12.

## Method

- Each case is one std function. Its **Ergo** form is the std call in an Ergo program, as a user writes it, built by `ergo build` from this checkout; its **Go** form is the Go std function in a plain Go program; its **hand** form, where there is one, is the Go a careful programmer writes instead (the plan names it).
- Each form runs the operation n times over 64 seeded inputs (input i mod 64) and folds each result into a checksum it prints. Every form of a case prints the same checksum at n = 1000 before any count is taken: Go is the oracle of the result.
- **Instructions**: user-space instructions retired by the process and its threads (`perf_event_open`, hardware instructions, kernel excluded, inherited, enabled at exec). GC and runtime work on other threads counts. Kernel work does not: the write system calls per operation (`syscw` of `/proc/self/io`) stand in for it.
- **Allocations**: `runtime.MemStats` `Mallocs` and `TotalAlloc`, printed at exit by a hook (`internal/runtime/exithook`) added to both programs' modules.
- **Per operation** = (count at N − count at N/4) / (3N/4): start-up and input set-up cancel. The median of 5 interleaved rounds; *spread* is (max − min) / median of the Ergo form's instruction slopes. `GOMAXPROCS=1`, `GODEBUG=asyncpreemptoff=1`.
- **Uses** is the number of times the case's call text occurs in `examples/**/*.ergo`: a static weight, not a run-time count.
- Counts are a receipt, not a time: they hold under host load. No test asserts a count.

Host: AMD EPYC 9554P 64-Core Processor, go version go1.27.0 linux/amd64, linux/amd64, load average 45.55 43.88 43.56 at the start and 38.55 42.26 43.03 at the end.

## Counts

Per operation. *Ergo/Go* is the instruction ratio; above 1.00 the Ergo form does more work than Go's.

| Package | Case | Uses | Ergo instr | Go instr | Ergo/Go | Hand instr | Ergo allocs (B) | Go allocs (B) | Hand allocs (B) | Spread | Note |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `strings` | Join | 128 | 1835 | 1805 | 1.02x | — | 2 (112) | 2 (112) | — | 9.4 % | Split and Join in one op: the parts of each input |
| `strings` | Split | 47 | 985 | 957 | 1.03x | — | 1 (71) | 1 (71) | — | 3.8 % | — |
| `strings` | Fields | 42 | 1979 | 1902 | 1.04x | — | 1 (61) | 1 (61) | — | 2.5 % | — |
| `strings` | HasPrefix | 79 | 24.0 | 22.0 | 1.09x | — | 0 (0) | 0 (0) | — | 0.5 % | — |
| `strings` | TrimSpace | 67 | 94.9 | 93.8 | 1.01x | — | 0 (0) | 0 (0) | — | 0.1 % | — |
| `strings` | Contains | 44 | 95.0 | 94.0 | 1.01x | — | 0 (0) | 0 (0) | — | 0.1 % | — |
| `strings` | Index | 14 | 85.5 | 85.5 | 1.00x | — | 0 (0) | 0 (0) | — | 0.1 % | — |
| `strings` | LastIndex | 1 | 107 | 106 | 1.01x | — | 0 (0) | 0 (0) | — | 0.0 % | — |
| `strings` | Cut | 20 | 141 | 115 | 1.23x | — | 0 (0) | 0 (0) | — | 0.0 % | — |
| `strings` | Slice | 103 | 16.0 | 16.0 | 1.00x | — | 0 (0) | 0 (0) | — | 0.3 % | — |
| `strings` | ReplaceAll | 45 | 1052 | 1028 | 1.02x | — | 0.98 (38) | 0.98 (38) | — | 5.5 % | — |
| `strings` | ToLower | 18 | 1410 | 1405 | 1.00x | — | 0.52 (19) | 0.52 (19) | — | 1.3 % | — |
| `strings` | Repeat | 26 | 571 | 549 | 1.04x | — | 1 (35) | 1 (35) | — | 12.5 % | — |
| `strings` | EqualFold | 5 | 56.3 | 55.3 | 1.02x | — | 0 (0) | 0 (0) | — | 0.1 % | — |
| `strconv` | interpolation (int, string, bool) | 1316 | 743 | 1971 | 0.38x | 236 | 0.27 (13) | 3 (60) | 0 (0) | 0.1 % | Go is fmt.Sprintf; hand Go appends into one buffer and makes one string |
| `strconv` | concat a + b + c | 418 | 316 | 316 | 1.00x | — | 0 (0) | 0 (0) | — | 0.0 % | — |
| `strconv` | Itoa | 12 | 251 | 248 | 1.01x | — | 1 (4) | 1 (4) | — | 0.2 % | — |
| `strconv` | Atoi (valid) | 39 | 158 | 139 | 1.14x | — | 0 (0) | 0 (0) | — | 0.0 % | — |
| `strconv` | Atoi (invalid) | 39 | 290 | 805 | 0.36x | — | 0.91 (15) | 1.91 (54) | — | 43.2 % | Go tells the two failures apart with errors.Is, as the Ergo catch does |
| `strconv` | ParseFloat | 4 | 414 | 399 | 1.04x | — | 0 (0) | 0 (0) | — | 0.0 % | — |
| `strconv` | FormatFloat | 3 | 881 | 871 | 1.01x | — | 1 (19) | 1 (19) | — | 1.0 % | — |
| `strconv` | Quote | 32 | 2894 | 2734 | 1.06x | — | 1.94 (85) | 1.28 (64) | — | 0.8 % | — |
| `prelude` | Map Get (string key) | 243 | 130 | 132 | 0.99x | — | 0 (0) | 0 (0) | — | 3.2 % | half the lookups miss |
| `prelude` | Map Put (string key, present) | 131 | 164 | 163 | 1.01x | — | 0 (0) | 0 (0) | — | 1.0 % | every key is present: an update in place |
| `prelude` | Map build (16 small-int keys) | 227 | 9477 | 9379 | 1.01x | 231 | 5 (936) | 5 (936) | 0 (0) | 0.3 % | a new map of 16 inserts per op; hand Go is the listable-key table G7 would emit (array and presence bits) |
| `prelude` | Map Get (small-int key) | 45 | 104 | 104 | 1.00x | 23.0 | 0 (0) | 0 (0) | 0 (0) | 0.6 % | every lookup hits; hand Go is the listable-key table G7 would emit; uses are the int-keyed map types |
| `maps` | Clone | 0 | 26999 | 4209 | 6.41x | — | 3 (2344) | 4 (2392) | — | 9.4 % | — |
| `maps` | Sorted(Keys) | 13 | 33706 | 26455 | 1.27x | — | 11 (1120) | 10 (1080) | — | 13.5 % | 64 keys |
| `slices` | Sort (64 ints) | 10 | 8184 | 8121 | 1.01x | — | 1 (512) | 1 (512) | — | 0.5 % | the input stays unsorted: Ergo's value rule copies it, Go copies by hand |
| `slices` | SortFunc (64 ints) | 8 | 15853 | 15769 | 1.01x | — | 1 (512) | 1 (512) | — | 0.2 % | descending; copies as Sort |
| `slices` | Reverse (64 ints) | 5 | 1020 | 965 | 1.06x | — | 1 (512) | 1 (512) | — | 0.9 % | copies as Sort |
| `slices` | Contains (64 ints) | 27 | 397 | 394 | 1.01x | — | 0 (0) | 0 (0) | — | 0.0 % | — |
| `slices` | Index (64 ints) | 3 | 448 | 387 | 1.16x | — | 0 (0) | 0 (0) | — | 0.1 % | — |
| `slices` | BinarySearch (64 ints) | 1 | 139 | 126 | 1.10x | — | 0 (0) | 0 (0) | — | 0.1 % | — |
| `slices` | Transform (64 ints) | 4 | 3296 | 1039 | 3.17x | — | 1 (512) | 1 (512) | — | 0.5 % | Go has no Transform: the Go form is the hand loop |
| `slices` | Filter (64 ints) | 3 | 5668 | 3157 | 1.80x | — | 6.44 (728) | 6.44 (728) | — | 0.5 % | Go has no Filter: the Go form is the hand loop (slices.DeleteFunc writes in place) |
| `utf8` | RuneCount | 5 | 277 | 276 | 1.00x | — | 0 (0) | 0 (0) | — | 0.0 % | — |
| `utf8` | DecodeRuneInString | 2 | 36.0 | 23.0 | 1.57x | — | 0 (0) | 0 (0) | — | 0.4 % | — |
| `hex` | Encode (1 KiB) | 2 | 25659 | 25249 | 1.02x | — | 2 (4096) | 2 (4096) | — | 0.3 % | Go is EncodeToString of the string's bytes, as a Go caller with a string writes it |
| `base64` | Encode (1 KiB) | 0 | 16341 | 16012 | 1.02x | — | 2 (2816) | 2 (2816) | — | 0.3 % | — |
| `sha256` | Sum (1 KiB) | 2 | 4147 | 4006 | 1.04x | — | 1 (1024) | 1 (1024) | — | 1.1 % | Go returns the digest as a string, as Ergo does |
| `crc32` | Checksum (1 KiB) | 0 | 1327 | 1211 | 1.10x | — | 1 (1024) | 1 (1024) | — | 2.1 % | — |
| `filepath` | Join (3 parts) | 3 | 1217 | 1205 | 1.01x | — | 1 (23) | 1 (23) | — | 2.1 % | Ergo passes one slice (no variadics); Go's variadic call builds one too |
| `filepath` | Clean | 0 | 1191 | 1173 | 1.01x | — | 1.66 (44) | 1.66 (44) | — | 1.8 % | — |
| `filepath` | Base | 0 | 96.5 | 95.5 | 1.01x | — | 0 (0) | 0 (0) | — | 0.0 % | — |
| `math` | Floor | 0 | 19.0 | 19.0 | 1.00x | — | 0 (0) | 0 (0) | — | 0.4 % | — |
| `bits` | RotateLeft32 | 0 | 29.0 | 28.0 | 1.04x | — | 0 (0) | 0 (0) | — | 0.3 % | — |
| `cmp` | Compare (int) | 4 | 14.0 | 14.0 | 1.00x | — | 0 (0) | 0 (0) | — | 0.6 % | — |
| `time` | Calendar | 1 | 352 | 309 | 1.14x | — | 0 (0) | 0 (0) | — | 0.1 % | Go reads three parts through three methods |
| `time` | Format (RFC 3339) | 32 | 743 | 704 | 1.05x | — | 1 (24) | 1 (24) | — | 1.4 % | — |
| `rand` | IntN (seeded PCG) | 63 | 114 | 66.0 | 1.73x | — | 0 (0) | 0 (0) | — | 0.0 % | Ergo's stream sits behind a lock (one stream per provider, shared by copies); Go's *rand.Rand has none |
| `io` | Println (to a file) | 209 | 854 | 1110 | 0.77x | 124 | 0.23 (11), 1 writes | 1 (16), 1 writes | 0 (0), 0.01 writes | 1.2 % | user-space instructions only; the write system calls per op are the syscw column |

## Weighted

Extra instructions per operation (Ergo − Go) times the uses, largest first: where a better std body or a general fact pays most across the examples. A negative value is a case where the Ergo form already does less work.

| Package | Case | Uses | Ergo − Go instr | Weighted |
| --- | --- | --- | --- | --- |
| `maps` | Sorted(Keys) | 13 | 7250 | 94254 |
| `prelude` | Map build (16 small-int keys) | 227 | 98 | 22236 |
| `slices` | Transform (64 ints) | 4 | 2257 | 9029 |
| `slices` | Filter (64 ints) | 3 | 2511 | 7533 |
| `strconv` | Quote | 32 | 160 | 5130 |
| `strings` | Join | 128 | 30 | 3783 |
| `strings` | Fields | 42 | 77 | 3237 |
| `rand` | IntN (seeded PCG) | 63 | 48 | 3023 |
| `strings` | Split | 47 | 28 | 1293 |
| `time` | Format (RFC 3339) | 32 | 38 | 1229 |
| `strings` | ReplaceAll | 45 | 24 | 1064 |
| `hex` | Encode (1 KiB) | 2 | 410 | 819 |
| `strconv` | Atoi (valid) | 39 | 19 | 741 |
| `slices` | SortFunc (64 ints) | 8 | 84 | 674 |
| `slices` | Sort (64 ints) | 10 | 63 | 632 |
| `strings` | Repeat | 26 | 22 | 578 |
| `strings` | Cut | 20 | 27 | 534 |
| `sha256` | Sum (1 KiB) | 2 | 141 | 282 |
| `slices` | Reverse (64 ints) | 5 | 56 | 278 |
| `slices` | Index (64 ints) | 3 | 61 | 183 |
| `strings` | HasPrefix | 79 | 2 | 159 |
| `prelude` | Map Put (string key, present) | 131 | 1 | 134 |
| `strings` | ToLower | 18 | 5 | 98 |
| `slices` | Contains (64 ints) | 27 | 3 | 80 |
| `strings` | TrimSpace | 67 | 1 | 70 |
| `strconv` | ParseFloat | 4 | 15 | 60 |
| `strings` | Contains | 44 | 1 | 44 |
| `time` | Calendar | 1 | 43 | 43 |
| `filepath` | Join (3 parts) | 3 | 11 | 34 |
| `strconv` | Itoa | 12 | 3 | 32 |
| `strconv` | FormatFloat | 3 | 10 | 29 |
| `utf8` | DecodeRuneInString | 2 | 13 | 26 |
| `slices` | BinarySearch (64 ints) | 1 | 13 | 13 |
| `utf8` | RuneCount | 5 | 1 | 5 |
| `strings` | EqualFold | 5 | 1 | 5 |
| `strings` | LastIndex | 1 | 1 | 1 |
| `strings` | Index | 14 | 0 | 0 |
| `strings` | Slice | 103 | 0 | 0 |
| `strconv` | concat a + b + c | 418 | 0 | 0 |
| `prelude` | Map Get (small-int key) | 45 | 0 | 0 |
| `maps` | Clone | 0 | 22790 | 0 |
| `base64` | Encode (1 KiB) | 0 | 330 | 0 |
| `crc32` | Checksum (1 KiB) | 0 | 116 | 0 |
| `filepath` | Clean | 0 | 18 | 0 |
| `filepath` | Base | 0 | 1 | 0 |
| `math` | Floor | 0 | 0 | 0 |
| `bits` | RotateLeft32 | 0 | 1 | 0 |
| `cmp` | Compare (int) | 4 | 0 | 0 |
| `prelude` | Map Get (string key) | 243 | -2 | -429 |
| `strconv` | Atoi (invalid) | 39 | -515 | -20079 |
| `io` | Println (to a file) | 209 | -256 | -53486 |
| `strconv` | interpolation (int, string, bool) | 1316 | -1228 | -1615925 |

## Cells

The text each measured package's "Lowering against hand Go" cell of `docs/spec/stdlib.md` (Std strengths over Effect) carries after `counted:`, from this run.

- `strings`: Join 1.02x of Go's instructions, 2 against 2 allocations; Split 1.03x of Go's instructions, 1 against 1 allocations; Fields 1.04x of Go's instructions, 1 against 1 allocations; HasPrefix 1.09x of Go's instructions, 0 against 0 allocations; TrimSpace 1.01x of Go's instructions, 0 against 0 allocations; Contains 1.01x of Go's instructions, 0 against 0 allocations; Index 1.00x of Go's instructions, 0 against 0 allocations; LastIndex 1.01x of Go's instructions, 0 against 0 allocations; Cut 1.23x of Go's instructions, 0 against 0 allocations; Slice 1.00x of Go's instructions, 0 against 0 allocations; ReplaceAll 1.02x of Go's instructions, 0.98 against 0.98 allocations; ToLower 1.00x of Go's instructions, 0.52 against 0.52 allocations; Repeat 1.04x of Go's instructions, 1 against 1 allocations; EqualFold 1.02x of Go's instructions, 0 against 0 allocations
- `strconv`: interpolation (int, string, bool) 0.38x of Go's instructions, 0.27 against 3 allocations (hand Go 0.12x instructions, 0 allocations); concat a + b + c 1.00x of Go's instructions, 0 against 0 allocations; Itoa 1.01x of Go's instructions, 1 against 1 allocations; Atoi (valid) 1.14x of Go's instructions, 0 against 0 allocations; Atoi (invalid) 0.36x of Go's instructions, 0.91 against 1.91 allocations; ParseFloat 1.04x of Go's instructions, 0 against 0 allocations; FormatFloat 1.01x of Go's instructions, 1 against 1 allocations; Quote 1.06x of Go's instructions, 1.94 against 1.28 allocations
- `prelude`: Map Get (string key) 0.99x of Go's instructions, 0 against 0 allocations; Map Put (string key, present) 1.01x of Go's instructions, 0 against 0 allocations; Map build (16 small-int keys) 1.01x of Go's instructions, 5 against 5 allocations (hand Go 0.02x instructions, 0 allocations); Map Get (small-int key) 1.00x of Go's instructions, 0 against 0 allocations (hand Go 0.22x instructions, 0 allocations)
- `maps`: Clone 6.41x of Go's instructions, 3 against 4 allocations; Sorted(Keys) 1.27x of Go's instructions, 11 against 10 allocations
- `slices`: Sort (64 ints) 1.01x of Go's instructions, 1 against 1 allocations; SortFunc (64 ints) 1.01x of Go's instructions, 1 against 1 allocations; Reverse (64 ints) 1.06x of Go's instructions, 1 against 1 allocations; Contains (64 ints) 1.01x of Go's instructions, 0 against 0 allocations; Index (64 ints) 1.16x of Go's instructions, 0 against 0 allocations; BinarySearch (64 ints) 1.10x of Go's instructions, 0 against 0 allocations; Transform (64 ints) 3.17x of Go's instructions, 1 against 1 allocations; Filter (64 ints) 1.80x of Go's instructions, 6.44 against 6.44 allocations
- `utf8`: RuneCount 1.00x of Go's instructions, 0 against 0 allocations; DecodeRuneInString 1.57x of Go's instructions, 0 against 0 allocations
- `hex`: Encode (1 KiB) 1.02x of Go's instructions, 2 against 2 allocations
- `base64`: Encode (1 KiB) 1.02x of Go's instructions, 2 against 2 allocations
- `sha256`: Sum (1 KiB) 1.04x of Go's instructions, 1 against 1 allocations
- `crc32`: Checksum (1 KiB) 1.10x of Go's instructions, 1 against 1 allocations
- `filepath`: Join (3 parts) 1.01x of Go's instructions, 1 against 1 allocations; Clean 1.01x of Go's instructions, 1.66 against 1.66 allocations; Base 1.01x of Go's instructions, 0 against 0 allocations
- `math`: Floor 1.00x of Go's instructions, 0 against 0 allocations
- `bits`: RotateLeft32 1.04x of Go's instructions, 0 against 0 allocations
- `cmp`: Compare (int) 1.00x of Go's instructions, 0 against 0 allocations
- `time`: Calendar 1.14x of Go's instructions, 0 against 0 allocations; Format (RFC 3339) 1.05x of Go's instructions, 1 against 1 allocations
- `rand`: IntN (seeded PCG) 1.73x of Go's instructions, 0 against 0 allocations
- `io`: Println (to a file) 0.77x of Go's instructions, 0.23 against 1 allocations (hand Go 0.11x instructions, 0 allocations)
