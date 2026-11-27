# Compiler DoD Floor Receipt

- Commit: `05d04ac3d`
- Measurement method: `internal/opcount` (`CountThread` in-process per-target + `Count` isolated child processes, `5` rounds alternating `GOGC=off` and `GOGC=100`, median and spread `(max-min)/median`, peak RSS from `VmHWM` / `ru_maxrss`).
- Reference baseline (`docs/receipts/self-host.md` at `e56dafb19`): weighted `fmt 191.5`, `parse 69.5` instr/byte across 98 targets (6,285,816 bytes); median target `fmt 234.6`, `parse 83.8`; `compiler` `fmt 265.39`, `parse 85.70`, `GC 10.8%`, `0.078 allocs/B`, `37.38 bytes/B`.

## Static DoD Invariants (`compiler/**`)

- Files scanned: `171`
- Structs scanned: `533`, fields scanned: `1907`, `Map` types scanned: `148`, functions scanned (`syntax`/`format`/`ir`): `698`
- Findings: **0 violations** (`no-pointer-keyed-maps`: PASS, `dense-uint32-ids`: PASS, `no-per-node-scratch-allocs`: PASS)

## Corpus DoD Summary

| Workload | Units | Base `GOGC=off` | New `GOGC=off` | Delta | Base `GOGC=100` | New `GOGC=100` | Base GC% | New GC% | Base `allocs/B` | New `allocs/B` | Base `bytes/B` | New `bytes/B` | New Spread (`off`) | Peak RSS (KiB) |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| format (all 119) | 12412248 | 217.21 | 251.98 | +16.00% | 229.96 | 261.62 | 5.5% | 3.7% | 0.0643 | 0.0155 | 32.46 | 21.17 | 0.05% | 202884 |
| format (median target) | 0 | 229.77 | 261.27 | +13.71% | 233.28 | 263.86 | 1.5% | 1.0% | 0.0765 | 0.0234 | 34.69 | 21.68 | 0.14% | - |
| internal/sketch/std | 6161086 | 194.03 | 224.67 | +15.80% | 207.41 | 237.27 | 6.5% | 5.3% | 0.0553 | 0.0120 | 29.16 | 19.44 | 0.06% | 202884 |
| compiler | 1996655 | 259.62 | 305.87 | +17.81% | 274.62 | 312.63 | 5.5% | 2.2% | 0.0685 | 0.0123 | 38.13 | 25.10 | 0.06% | 202884 |
| examples | 4254507 | 230.88 | 266.22 | +15.31% | 241.21 | 272.00 | 4.3% | 2.1% | 0.0752 | 0.0220 | 34.57 | 21.81 | 0.03% | 202884 |
| syntax (all 119) | 12412248 | 79.23 | 82.31 | +3.88% | 84.22 | 86.02 | 5.9% | 4.3% | 0.0048 | 0.0040 | 27.66 | 22.93 | 0.21% | 202892 |
| syntax (median target) | 0 | 84.41 | 86.18 | +2.11% | 85.12 | 87.16 | 0.8% | 1.1% | 0.0089 | 0.0074 | 29.40 | 24.08 | 0.36% | - |
| internal/sketch/std | 6161086 | 72.97 | 75.75 | +3.80% | 77.93 | 79.05 | 6.4% | 4.2% | 0.0032 | 0.0028 | 24.49 | 20.39 | 0.21% | 202892 |
| compiler | 1996655 | 89.29 | 94.44 | +5.77% | 95.53 | 101.03 | 6.5% | 6.5% | 0.0030 | 0.0025 | 33.28 | 28.30 | 0.23% | 202892 |
| examples | 4254507 | 83.53 | 86.12 | +3.11% | 87.45 | 89.22 | 4.5% | 3.5% | 0.0079 | 0.0066 | 29.62 | 24.10 | 0.22% | 202892 |
| ir-write | 11097084 | 54.08 | 49.17 | -9.07% | 58.25 | 52.18 | 7.2% | 5.8% | 0.0612 | 0.0313 | 9.19 | 7.70 | 0.10% | 121546 |
| checkdump | 630656 | 3763.56 | 3751.22 | -0.33% | 5393.09 | 5327.74 | 30.2% | 29.6% | - | - | - | - | 0.13% | 243406 |
| irdump | 630656 | 14841.71 | 14568.42 | -1.84% | 30836.70 | 30162.42 | 51.9% | 51.7% | - | - | - | - | 0.44% | 2077388 |
| nativebuild | 1149 | 26125.15 | 25185.82 | -3.60% | 49384.80 | 40656.41 | 47.1% | 38.1% | - | - | - | - | 0.45% | 97140 |

## Format Targets (Per Target)

```
target                              files     bytes       fmt    fmt-gc     gc%  allocs/B   bytes/B
internal/sketch/std                   561   6161086    224.67    237.27    5.3%    0.0120     19.44
compiler                              171   1996655    305.87    312.63    2.2%    0.0123     25.10
examples/address-routes                 9     37754    273.46    273.80    0.1%    0.0177     22.23
examples/admit                          9     22085    245.71    246.00    0.1%    0.0302     20.87
examples/adventure                     10     31961    268.86    271.94    1.1%    0.0211     23.20
examples/backups                       15     47441    265.45    265.15   -0.1%    0.0234     22.01
examples/board                          2     12734    217.05    217.60    0.3%    0.0090     20.31
examples/bookmarks                      5     24247    270.25    270.38    0.0%    0.0205     22.31
examples/bucketstore                    3      5373    301.16    303.50    0.8%    0.0421     26.21
examples/calc                          17     47999    277.13    277.40    0.1%    0.0258     22.94
examples/canvas-scenes                 10     60647    298.12    298.45    0.1%    0.0205     23.57
examples/catalog                        4     19858    259.59    260.04    0.2%    0.0234     21.67
examples/cborpack                       5     13572    241.36    240.85   -0.2%    0.0337     20.14
examples/cells                         11     91114    310.00    309.92   -0.0%    0.0180     25.04
examples/chat                           8     37412    237.34    237.66    0.1%    0.0201     20.09
examples/clientgen                      1      7076    226.98    227.92    0.4%    0.0129     17.78
examples/confparse                     13     52535    275.68    275.74    0.0%    0.0231     22.82
examples/counter                        3      1777    276.08    277.30    0.4%    0.1283     24.51
examples/counter-gui                    5     12053    270.97    271.31    0.1%    0.0308     23.22
examples/counter-list                   8     21218    270.75    270.98    0.1%    0.0316     22.03
examples/cron                           6     19327    242.56    243.15    0.2%    0.0235     20.88
examples/csvreport                     10     28515    237.35    238.01    0.3%    0.0257     19.99
examples/dash-gui                      14     51916    267.87    267.99    0.0%    0.0213     21.68
examples/deploys                       17     58937    236.61    236.83    0.1%    0.0232     19.63
examples/deps                          12     54760    268.75    269.21    0.2%    0.0203     22.55
examples/domain-search                 24    150131    252.69    252.18   -0.2%    0.0155     20.14
examples/download                       4      8351    252.22    252.77    0.2%    0.0310     22.86
examples/downloads                      5     30287    248.29    248.67    0.2%    0.0185     21.15
examples/draw-gui                      19     70517    308.82    308.31   -0.2%    0.0298     24.56
examples/echo-chat                      7     33670    250.50    250.63    0.1%    0.0236     21.33
examples/editions                       1       706    257.75    260.80    1.2%    0.0765     26.33
examples/editor-gui                    16     75877    280.70    280.44   -0.1%    0.0197     22.28
examples/elevator                       4     14526    283.20    283.80    0.2%    0.0233     23.66
examples/events                         5      6726    259.62    261.24    0.6%    0.0425     22.53
examples/extract                        5     12559    239.27    239.01   -0.1%    0.0273     19.79
examples/fetchpool                      4     12015    284.91    285.67    0.3%    0.0311     25.66
examples/fieldnotes                     9     29570    257.83    265.60    2.9%    0.0237     21.12
examples/flattree                      17     60145    290.28    289.98   -0.1%    0.0233     23.87
examples/fluid-gallery                 10     45416    323.78    324.16    0.1%    0.0207     25.87
examples/forge                         11     43661    259.46    259.81    0.1%    0.0220     21.19
examples/gallery                       10     43681    268.99    268.57   -0.2%    0.0193     21.95
examples/gent                          14     98835    248.86    248.69   -0.1%    0.0180     19.91
examples/gointerop                      9     21217    244.32    244.77    0.2%    0.0303     20.63
examples/idle-timer                     9     29009    233.05    233.37    0.1%    0.0227     19.69
examples/inbox                          2      6878    294.32    295.41    0.4%    0.0230     24.27
examples/jobq                          12     48553    284.43    284.67    0.1%    0.0221     23.23
examples/jobs                          13     45475    262.22    262.03   -0.1%    0.0289     21.37
examples/jobs-web                      12     53216    250.02    250.36    0.1%    0.0214     21.50
examples/jsonparse                     19     54923    279.19    279.00   -0.1%    0.0242     22.99
examples/jsonv                         15     51426    256.15    256.50    0.1%    0.0250     20.99
examples/keypad                        11     33159    246.70    246.76    0.0%    0.0229     21.36
examples/kmeans                        11     25358    263.47    263.86    0.1%    0.0287     22.36
examples/kvstore                       17     52975    246.01    246.18    0.1%    0.0277     19.96
examples/lease-sync                    20    113116    268.02    267.69   -0.1%    0.0193     21.38
examples/limiter                       13     65269    248.09    247.76   -0.1%    0.0192     20.70
examples/lint-policy                    4      9235    213.58    214.67    0.5%    0.0228     16.86
examples/logstat                       14     39635    260.37    260.74    0.1%    0.0260     21.45
examples/lookup                         4      3281    220.99    222.60    0.7%    0.0677     20.45
examples/lru                            8     33571    306.75    307.41    0.2%    0.0244     24.42
examples/lzpack                        11     29226    261.27    261.99    0.3%    0.0255     21.66
examples/markup                         2      3427    295.99    297.25    0.4%    0.0423     24.89
examples/mdhtml                        10    115734    259.61    260.88    0.5%    0.0126     20.67
examples/metrics                        7     26495    279.64    279.93    0.1%    0.0237     22.69
examples/model-paths                   10     59233    285.02    285.16    0.0%    0.0122     23.26
examples/orchestrate                    2     10026    177.96    178.37    0.2%    0.0102     16.02
examples/pager                          2      3546    299.51    301.10    0.5%    0.0477     25.40
examples/pairing                        7     27956    261.46    261.89    0.2%    0.0232     21.98
examples/particles                     16     42272    281.83    281.92    0.0%    0.0369     23.20
examples/permit-wizard                  7     35005    252.89    252.86   -0.0%    0.0155     20.82
examples/pipeline                      32    198224    249.38    249.19   -0.1%    0.0174     20.71
examples/pure-eval                      3      8086    235.48    236.47    0.4%    0.0194     19.50
examples/query-table                   11     55339    267.86    267.64   -0.1%    0.0162     21.82
examples/quiz                           9     28143    238.27    239.26    0.4%    0.0213     19.90
examples/quota                         14     45297    247.61    247.98    0.2%    0.0244     21.24
examples/raytrace                       8     28798    326.86    326.94    0.0%    0.0361     25.01
examples/regex                         19    102587    258.49    258.09   -0.2%    0.0176     20.88
examples/relay                          5      4598    236.56    236.65    0.0%    0.0657     21.08
examples/respd                         13     62797    269.16    269.56    0.1%    0.0193     21.56
examples/ritmo                         11     43092    259.21    259.69    0.2%    0.0255     20.74
examples/routecli                       3      3446    291.69    292.77    0.4%    0.0482     26.08
examples/router                         8     61372    252.60    252.17   -0.2%    0.0147     20.26
examples/router-model                   3     11457    232.91    232.32   -0.3%    0.0157     19.70
examples/runtime-faults                 6     32380    249.20    249.40    0.1%    0.0170     20.01
examples/scoped-engine                  9     27802    250.58    250.69    0.0%    0.0290     20.80
examples/scopes                         2      5188    284.78    286.22    0.5%    0.0312     25.03
examples/search                         5     22647    252.52    252.84    0.1%    0.0217     21.30
examples/shards                         8     25316    277.73    277.98    0.1%    0.0251     23.36
examples/signup-form                    9     28864    232.94    233.37    0.2%    0.0222     19.48
examples/snake                          8     25599    278.27    278.63    0.1%    0.0269     22.76
examples/sorted-insert                  3      2799    259.31    261.03    0.7%    0.0557     22.04
examples/ssg                           13     41748    246.45    245.84   -0.3%    0.0235     20.73
examples/stack-builder                  8     25313    306.10    306.33    0.1%    0.0327     25.85
examples/stackvm                       11     58879    290.37    289.75   -0.2%    0.0165     23.12
examples/stars-gui                     16     62523    263.18    263.61    0.2%    0.0191     21.36
examples/starter                        4      6759    249.19    249.57    0.2%    0.0348     20.68
examples/station-stats                 10     24405    251.78    252.25    0.2%    0.0317     20.52
examples/stopwatch                      7     28197    238.17    238.74    0.2%    0.0217     18.64
examples/sudoku                         9     25303    240.47    240.75    0.1%    0.0256     19.85
examples/tally-board                    2      9954    289.31    290.07    0.3%    0.0136     23.64
examples/task-board                    12     72023    279.75    303.72    7.9%    0.0197     22.01
examples/tasks-cli                     17     37921    249.07    249.52    0.2%    0.0298     20.32
examples/tidewalk-replay               11     14474    272.16    272.87    0.3%    0.0438     23.45
examples/timetable                      8     46339    276.48    276.78    0.1%    0.0156     22.17
examples/tn1                            2     10639    277.47    277.59    0.0%    0.0190     23.17
examples/tn1-lint                       2      8545    301.54    301.63    0.0%    0.0297     26.80
examples/todo-gui                       5     15856    279.58    279.99    0.1%    0.0230     23.30
examples/todo-shell                    10     23695    254.10    254.86    0.3%    0.0303     22.17
examples/toktree                        3     12683    306.23    306.26    0.0%    0.0311     24.72
examples/tomlparse                     13     52752    291.37    291.77    0.1%    0.0203     23.69
examples/transfers                     11     45516    258.69    258.67   -0.0%    0.0290     21.02
examples/triage                         5     11880    239.20    239.10   -0.0%    0.0258     19.06
examples/tui                            8     21290    256.55    256.79    0.1%    0.0280     21.06
examples/twig                          18     77246    310.16    310.17    0.0%    0.0197     25.68
examples/uitree                         2      4065    278.05    279.27    0.4%    0.0327     24.36
examples/vt                            20    106608    279.00    278.90   -0.0%    0.0211     23.12
examples/weather                       10     35504    227.90    228.00    0.0%    0.0259     19.10
examples/wire-config                    2       920    294.32    294.32    0.0%    0.0870     26.37
examples/work                           1      6187    202.97    203.68    0.4%    0.0170     16.85
examples/wtcache                        5      7252    245.83    246.68    0.3%    0.0564     22.66
```
