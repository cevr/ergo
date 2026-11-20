# Self-host receipts

Counted receipts of the self-hosting capstone (plan: capstone §9). Counts are user-mode
instructions of the calling thread (`internal/opcount`), in process, 5 rounds, median, niced, under
load. No wall time decides anything.

## S1 format

The format receipt (capstone S1b, the old SH2), taken at e56dafb19 on wf10/s2-syntax.

**What changed since the plan.** The plan's modes `go` (the Go formatter) and `encode` (the bridge
to it) no longer exist. The Go formatter was deleted at S1c, the bridge at S2d, and `landedStage`
is 2. The kill line that was to decide S1c is past, because S1c landed. So this receipt counts what
`ergo fmt` runs today:
- `fmt`: `format.Source`, which parses with `compiler/syntax` and prints with `compiler/format`;
- `parse`: its parse alone.

The Go product's ratio comes from the S1d receipt below, which measured it before the Go formatter
was deleted.

**Targets.** `internal/sketch/std`, `compiler/`, and each folder of `examples/` that has Ergo
sources (testdata excluded). Every `.ergo` file of a target is formatted once per round.

**Columns.**
- `fmt` and `parse`: instructions per source byte, with the collector off (GOGC=off).
- `fmt-gc`: `fmt` with GOGC=100. `gc%` is the collector's share, (`fmt-gc` − `fmt`) / `fmt-gc`. A
  negative value is noise, under 2.5% everywhere.
- `allocs/B` and `bytes/B`: allocations and allocated bytes per source byte, over one pass.

### Summary (measured)

| | instr/byte |
| --- | --- |
| all 98 targets, weighted by bytes (6,285,816 bytes) | fmt 191.5, parse 69.5 |
| median target | fmt 234.6, parse 83.8 |
| `internal/sketch/std` (2.9 MB) | fmt 142.8, parse 54.5, GC 7.5% |
| `compiler/` | fmt 265.4, parse 85.7, GC 10.8% |
| smallest folders (under 1 KB) | up to 403: a fixed cost per file, spread over few bytes |

- **Per file:** printing costs about 1.8 times the parse on the median target (fmt/parse 2.78).
- **Allocations:** 0.08 allocations and 33 bytes per source byte at the median. The output text is
  one of them.
- **Against the Go product:** the floor corpus of r7 (1,328 files of `compiler/`, `examples/` and
  std) is the one set with a measured Go product. There, `ergo fmt` is 187.8 instr/byte against the
  Go product's 336.3: **0.56x**, and 1.26x the hand-Go floor of 149.0 (S2-syntax r10 receipt). The
  Go product is Ergo parse, Go decode and the Go formatter, measured at 2956d0ef7 before S1c.
- **Work growth:** `TestFormatWorkGrowsLinearly` (`internal/selfhostcheck`) holds the printer's
  binary walks to at most 2.2x from 200 to 400 operands of one chain, and it passes. With the
  memo dropped (mutant `sh-walk-memo-dropped`) the growth is 3.98x.

### Per target (measured, e56dafb19)

```
target                              files     bytes      fmt    parse   fmt-gc     gc% allocs/B  bytes/B
internal/sketch/std                   334   2938418   142.81    54.54   154.35    7.5%    0.044    21.04
compiler                               34    336286   265.39    85.70   297.52   10.8%    0.078    37.38
examples/adventure                     11     21999   234.24    82.60   233.20   -0.4%    0.091    31.25
examples/backups                       14     38107   229.43    83.15   227.73   -0.7%    0.082    33.03
examples/bookmarks                      5     16928   233.64    86.69   234.17    0.2%    0.077    32.96
examples/bucketstore                    3      3269   335.91   132.88   336.49    0.2%    0.130    39.00
examples/calc                          20     47805   244.96    85.21   243.46   -0.6%    0.093    36.01
examples/catalog                        3     10891   224.58    78.92   225.28    0.3%    0.081    28.71
examples/cborpack                       4     10831   217.71    76.00   214.08   -1.7%    0.084    24.33
examples/chat                           8     27796   204.71    72.00   203.39   -0.7%    0.072    26.83
examples/confparse                     15     52082   236.77    84.90   233.48   -1.4%    0.080    34.43
examples/counter                        3      1739   317.72   149.55   318.68    0.3%    0.186    29.64
examples/counter-gui                    4      8747   260.83   104.50   261.54    0.3%    0.091    36.26
examples/cron                           8     29832   221.46    80.63   219.00   -1.1%    0.071    32.12
examples/csvreport                     10     24675   215.44    75.93   215.91    0.2%    0.076    28.11
examples/dash-gui                      13     44577   238.59    84.87   238.44   -0.1%    0.080    34.62
examples/deploys                       23     59299   208.73    73.91   208.53   -0.1%    0.076    28.21
examples/deps                          18     58943   237.38    83.75   238.33    0.4%    0.087    35.22
examples/download                       3      4956   243.60   102.04   244.35    0.3%    0.097    36.15
examples/downloads                      7     30022   211.60    75.69   211.10   -0.2%    0.074    29.93
examples/draw-gui                      17     62788   264.01    90.14   264.44    0.2%    0.092    38.24
examples/editions                       1       686   403.08   236.24   405.22    0.5%    0.149    34.64
examples/editor-gui                    14     69631   244.52    83.10   244.80    0.1%    0.080    35.76
examples/events                         5      6683   262.19   100.27   256.85   -2.1%    0.099    35.23
examples/fetchpool                      4      8757   266.81   101.87   267.41    0.2%    0.102    39.76
examples/fk-board                      15     47277   235.52    83.74   236.17    0.3%    0.081    34.14
examples/fk-chat                        6     21759   222.06    80.20   219.14   -1.3%    0.083    31.45
examples/fk-counters                    6     11642   247.34    97.60   248.00    0.3%    0.093    32.76
examples/fk-diag                        7     26762   212.14    78.84   213.51    0.6%    0.074    30.29
examples/fk-domains                    26    130787   215.00    74.03   215.23    0.1%    0.066    31.50
examples/fk-engine                      7     16868   212.90    81.17   215.96    1.4%    0.084    29.75
examples/fk-flow                       10     32431   255.67    87.11   257.89    0.9%    0.088    37.24
examples/fk-form                        7     19130   207.27    77.41   206.79   -0.2%    0.078    26.39
examples/fk-gallery                     7     22430   238.70    85.43   233.44   -2.3%    0.079    33.57
examples/fk-routes                     12     27064   234.55    83.64   230.98   -1.5%    0.085    31.49
examples/fk-stopwatch                   6     16962   205.59    79.45   207.71    1.0%    0.078    25.50
examples/fk-table                       8     23142   233.79    84.69   235.69    0.8%    0.077    33.67
examples/fk-transfers                   9     25538   220.02    80.77   219.69   -0.1%    0.083    31.03
examples/fk-weather                     8     24685   184.47    64.75   184.21   -0.1%    0.069    22.04
examples/fk-wizard                      6     19595   210.88    75.90   208.37   -1.2%    0.078    27.99
examples/flattree                      21     68173   252.20    86.09   253.10    0.4%    0.095    36.42
examples/forge                         14     43438   229.09    83.18   227.43   -0.7%    0.084    33.06
examples/gointerop                      7     16562   224.79    79.21   224.35   -0.2%    0.081    29.93
examples/idle-timer                     9     28259   206.63    71.91   206.72    0.0%    0.077    29.41
examples/inbox                          3      6779   288.97   104.67   294.40    1.8%    0.088    34.72
examples/jobq                          14     41440   244.67    88.12   244.40   -0.1%    0.083    36.41
examples/jobs                          14     43607   219.61    77.18   218.25   -0.6%    0.082    30.15
examples/jobs-web                       9     26065   214.29    78.92   215.94    0.8%    0.083    29.17
examples/jsonparse                     20     47649   253.53    91.20   256.88    1.3%    0.094    37.24
examples/jsonv                         17     51088   217.87    79.07   219.23    0.6%    0.081    31.94
examples/keypad                        11     32479   219.69    81.85   219.46   -0.1%    0.081    33.68
examples/kmeans                        10     23861   240.06    85.48   239.95   -0.0%    0.088    34.18
examples/kvstore                       16     38274   208.90    72.67   206.87   -1.0%    0.083    27.21
examples/limiter                        9     31197   207.57    74.71   205.38   -1.1%    0.077    28.61
examples/lint-policy                    4      9088   199.09    77.64   197.98   -0.6%    0.058    23.09
examples/logstat                       17     39415   232.06    83.31   231.32   -0.3%    0.083    31.39
examples/lookup                         4      3215   236.37   107.18   248.31    4.8%    0.117    25.86
examples/lru                           11     28422   257.40    92.63   257.07   -0.1%    0.098    36.75
examples/lzpack                        11     29152   228.36    84.25   226.84   -0.7%    0.077    33.63
examples/markup                         3      3426   323.40   150.73   323.69    0.1%    0.120    33.55
examples/mdhtml                        12    113927   217.92    72.06   216.77   -0.5%    0.057    30.96
examples/metrics                        8     22522   238.87    92.94   249.17    4.1%    0.083    34.78
examples/okra-keys                     14     30568   204.46    75.09   206.80    1.1%    0.079    28.06
examples/onebrc                         8     20167   237.48    83.11   236.15   -0.6%    0.080    30.50
examples/pager                          3      3537   299.81   124.57   299.93    0.0%    0.119    35.21
examples/pairing                        9     27324   228.95    84.93   228.07   -0.4%    0.091    34.96
examples/particles                     17     38289   243.50    88.26   241.70   -0.7%    0.091    32.06
examples/pipeline                      31    142665   205.62    71.54   202.63   -1.5%    0.071    29.41
examples/pure-eval                      4      8505   226.49    82.94   227.24    0.3%    0.078    28.88
examples/quiz                           9     27656   215.24    79.34   213.69   -0.7%    0.075    29.94
examples/quota                         16     41710   220.74    77.60   219.91   -0.4%    0.081    31.23
examples/raytrace                      10     28062   282.37    98.70   282.49    0.0%    0.097    38.93
examples/regex                         21    100336   218.45    75.77   217.94   -0.2%    0.073    31.25
examples/relay                          5      4514   237.34    97.08   237.71    0.2%    0.125    26.95
examples/routecli                       3      2903   336.02   162.12   348.07    3.5%    0.116    37.62
examples/router                         8     45611   205.15    72.29   202.58   -1.3%    0.069    28.03
examples/router-model                   3     11076   217.00    86.94   217.22    0.1%    0.070    28.55
examples/scopes                         3      5097   274.04   112.13   274.81    0.3%    0.098    36.55
examples/search                         7     22489   219.42    77.44   221.32    0.9%    0.079    31.14
examples/shards                         9     24586   244.89    91.77   246.59    0.7%    0.096    35.78
examples/snake                          9     25255   243.36    89.46   244.98    0.7%    0.085    35.03
examples/ssg                           14     42031   216.16    78.10   215.38   -0.4%    0.075    31.18
examples/stack-builder                  9     24654   265.12    97.13   263.62   -0.6%    0.104    38.86
examples/stackvm                       13     33368   262.59    92.44   261.66   -0.4%    0.093    37.87
examples/stars-gui                     14     55997   227.71    79.42   226.15   -0.7%    0.077    33.32
examples/starter                        5      6751   258.83   112.13   257.56   -0.5%    0.092    32.59
examples/sudoku                        11     25414   220.61    81.27   222.74    1.0%    0.077    29.19
examples/tasks-cli                     20     37780   224.68    83.16   228.35    1.6%    0.087    31.87
examples/tidewalk-replay               12     14276   260.72    94.44   257.32   -1.3%    0.113    37.84
examples/todo-gui                       4     13379   255.94    96.99   255.35   -0.2%    0.078    36.88
examples/todo-shell                    12     23323   236.63    84.85   235.75   -0.4%    0.094    32.36
examples/toktree                        5     12880   278.97    99.13   279.12    0.1%    0.094    38.84
examples/tomlparse                     15     51989   254.69    86.96   253.25   -0.6%    0.082    35.60
examples/tui                           11     20940   237.49    84.21   236.14   -0.6%    0.091    32.66
examples/twig                          21     76670   261.87    91.41   261.61   -0.1%    0.088    37.55
examples/uitree                         3      3989   307.71   117.98   307.55   -0.1%    0.102    32.81
examples/vt                            19    101264   237.62    80.20   235.19   -1.0%    0.070    33.82
examples/wire-config                    2       904   401.65   231.24   403.95    0.6%    0.156    39.12
```

### Path to the floor

The owner is lane A. These are inferred from the S2-syntax r10 and S2-STREAM stage counts.
- Printing is 120 of the 187 on the floor corpus, and the parse 66. The floor is 149: scan 28,
  structure 18, analyze 30, emit and render 73.
- A streaming printer was tried and deleted. Its own passes cost 189 even with the parse taken as
  free (S2-STREAM).
- So the cuts left are inside the tree printer:
  - its comment and line lookups, measured as a share of each print in S1d;
  - the per-file fixed cost, which dominates folders under 4 KB.
