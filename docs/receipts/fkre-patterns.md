# FKre: a refinement's pattern against Go's regexp

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

## Cost

The matcher is code, one `case` per state of the minimal automaton: Email's has 6 states, Date's 11, and Label's 125, since `{0,61}` counts and each count is reached with a hyphen last or not. The three together are 1,273 lines of formatted Go. A repeat's count is at most 100 and an automaton at most 2,000 states (Refinements 11), which bounds that size.

## Prior art

Go's regexp compiles at start and interprets a one-pass, backtracking or NFA program per match: the right column. re2c and Ragel write a pattern's DFA as straight-line code at build time, as this lowering does. Rust's `regex-automata` can build a dense DFA ahead of time and serialise it as a table. Elixir's `~r` compiles at compile time to a PCRE program but still runs PCRE's interpreter. Gleam and ReScript hand the pattern to the host's engine.
