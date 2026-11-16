# FKre: a refinement's pattern against Go's regexp, and its two forms

Measured head: the FKre branch at `33d54577b` plus the bench in `docs/research/pattern-bench` (types.md, Refinements 11). The commit that adds this receipt adds the bench and this file only, so it changes no measured code.

The bench compares, on the same inputs, the Go matcher the lowering writes for a pattern (`lower.MatcherText`, written into `matchers.go` by `go run ./docs/research/pattern-bench/gen`) with `regexp.MustCompile("^(?:p)$").MatchString`. It checks first that both say the same of every input. The counted measures are allocations per match (`-benchmem`) and the CPU's user-space instruction count per match (`perf_event_open`, `PERF_COUNT_HW_INSTRUCTIONS`, the goroutine locked to its thread). Times are shown but are not the claim: the host was shared with other lanes.

```
go run ./docs/research/pattern-bench/gen
GOMAXPROCS=2 go test -run '^$' -bench . -benchmem -count 3 ./docs/research/pattern-bench
```

## Patterns and inputs

| Name | Pattern | Inputs (matching and not) |
| --- | --- | --- |
| Email | fk-form's upstream pattern `[^\s@]+@[^\s@]+\.[^\s@]+` | 5 |
| Label | fk-domains' `DomainLabel`, `[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?` | 6 |
| Date | `\d{4}-\d{2}-\d{2}` | 5 |

## Counted results (2026-11-13, AMD EPYC 9554P, go1.27.0 linux/amd64, GOMAXPROCS=2)

The instruction counts were the same in all three runs, to the instruction for the matcher.

| Pattern | Matcher instr/match | regexp instr/match | Ratio | Allocs/match (both) | MustCompile at start, removed |
| --- | --- | --- | --- | --- | --- |
| Email | 481.8 | 4,983 | 0.097 | 0 | 70 allocs, 5,792 B, about 41,000 instr |
| Label | 152.0 | 2,352 | 0.065 | 0 | 243 allocs, 44,232 B, about 170,000 instr |
| Date | 172.8 | 970.6 | 0.178 | 0 | 73 allocs, 6,024 B, about 44,000 instr |

Neither allocates per match. The matcher takes 6 to 18 percent of regexp's instructions per match, and the program does no `MustCompile` when it starts. Time per match followed the counts (Email about 55 against 530 ns, Label 18 against 230 ns, Date 33 against 120 ns), under load.

## Two forms (FKre-2)

The straight-line matcher is code, one `case` per state of the minimal automaton, so it grows with the automaton: Label's has 125 states, since `{0,61}` counts and each count is reached with a hyphen last or not. FKre-2 adds a table form (`lower.MatcherTableText`, pattern_table.go): a class per set of runes no state tells apart, a `[128]` class map for ASCII, a binary search over the spans past ASCII, and a transition table, all in one package variable, read by one loop. The lowering writes the table past `PatternTableStates` states. These numbers chose that threshold.

Measured head: wf10/fkre2 with the bench of this commit, whose `gen` writes both forms of each pattern (`matchNAME`, `matchNAMETable`). Bytes are `go tool nm -size` of the bench's test binary, which links every form (`go run ./docs/research/pattern-bench/size`): the function's code, and for the table also its variable. Instructions are counted as above, 3 runs, equal to the instruction in each. The Label row's inputs now include a 63-byte label, so its count differs from the table above.

| Pattern | States | Straight-line bytes | Table bytes (code + data) | Straight-line instr/match | Table instr/match | regexp instr/match |
| --- | --- | --- | --- | --- | --- | --- |
| Email `[^\s@]+@[^\s@]+\.[^\s@]+` | 6 | 567 | 425 (261 + 164) | 481.8 | 449.6 | 4,984 |
| Date `\d{4}-\d{2}-\d{2}` | 11 | 436 | 456 (276 + 180) | 172.8 | 276.8 | 970.6 |
| Word `[a-z]{1,12}` | 13 | 575 | 433 (261 + 172) | 134.4 | 192.0 | 1,524 |
| Word20 `[a-z]{1,20}` | 21 | 841 | 459 (263 + 196) | 213.6 | 318.2 | 2,482 |
| Ident `[A-Za-z_][A-Za-z0-9_]{0,23}` | 25 | 1,535 | 512 (276 + 236) | 240.6 | 272.8 | 2,799 |
| UUID | 37 | 1,542 | 564 (280 + 284) | 357.5 | 575.0 | 1,474 |
| SHA1 `[0-9a-f]{40}` | 41 | 1,742 | 526 (270 + 256) | 406.8 | 569.0 | 1,581 |
| Label | 125 | 7,118 | 929 (293 + 636) | 329.7 | 418.7 | 3,982 |

**The threshold: 24 states.** The table's bytes stay between about 420 and 930, and its code is the same loop, so its size barely grows with the automaton. The straight-line code grows with the states and their spans. Up to Word20's 21 states neither form wins clearly. The table is 1.3 to 1.8 times smaller, except Date's, where they are even, and the straight-line form takes 1.4 to 1.6 times fewer instructions, except Email's, where the table takes 7 percent fewer. From Ident's 25 states the table is 2.7 to 7.7 times smaller and costs 1.1 to 1.6 times the instructions, still 0.1 to 0.4 times regexp's. So the lowering writes the straight-line form up to 24 states and the table past that: fk-form's Email stays straight-line, and fk-domains' Label becomes a table. Both forms allocate nothing per match.

A repeat's count is at most 100 and an automaton at most 2,000 states (Refinements 11), which bounds the table: at most 2,000 times the classes cells, of `uint8`, or of `uint16` past 254 states.

## Prior art

Go's regexp compiles at start and interprets a one-pass, backtracking or NFA program per match: the right column. re2c and Ragel write a pattern's DFA as straight-line code at build time, as this lowering does. Rust's `regex-automata` can build a dense DFA ahead of time and serialise it as a table. Elixir's `~r` compiles at compile time to a PCRE program but still runs PCRE's interpreter. Gleam and ReScript hand the pattern to the host's engine.
