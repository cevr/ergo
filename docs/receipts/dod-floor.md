# Compiler DoD Floor Receipt

- Commit: `36d9836b9`
- Measurement method: `internal/opcount` (`CountThread` in-process per-target + `Count` isolated child processes, `5` rounds alternating `GOGC=off` and `GOGC=100`, median and spread `(max-min)/median`, peak RSS from `VmHWM` / `ru_maxrss`).
- Reference baseline (`docs/receipts/self-host.md` at `e56dafb19`): weighted `fmt 191.5`, `parse 69.5` instr/byte across 98 targets (6,285,816 bytes); median target `fmt 234.6`, `parse 83.8`; `compiler` `fmt 265.39`, `parse 85.70`, `GC 10.8%`, `0.078 allocs/B`, `37.38 bytes/B`.

## Static DoD Invariants (`compiler/**`)

- Files scanned: `171`
- Structs scanned: `533`, fields scanned: `1907`, `Map` types scanned: `148`, functions scanned (`syntax`/`format`/`ir`): `712`
- Findings: **0 violations** (`no-pointer-keyed-maps`: PASS, `dense-uint32-ids`: PASS, `no-per-node-scratch-allocs`: PASS)

## Corpus DoD Summary

| Workload | Units | Base `GOGC=off` | New `GOGC=off` | Delta | Base `GOGC=100` | New `GOGC=100` | Base GC% | New GC% | Base `allocs/B` | New `allocs/B` | Base `bytes/B` | New `bytes/B` | New Spread (`off`) | Peak RSS (KiB) |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| format (all 119) | 12490163 | 217.21 | 200.20 | -7.83% | 229.96 | 208.73 | 5.5% | 4.1% | 0.0643 | 0.0152 | 32.46 | 20.35 | 0.05% | 187500 |
| format (median target) | 0 | 229.77 | 209.70 | -8.73% | 233.28 | 210.64 | 1.5% | 0.4% | 0.0765 | 0.0230 | 34.69 | 20.64 | 0.31% | - |
| internal/sketch/std | 6236077 | 194.03 | 179.63 | -7.42% | 207.41 | 190.57 | 6.5% | 5.7% | 0.0553 | 0.0118 | 29.16 | 18.85 | 0.05% | 187500 |
| compiler | 1998593 | 259.62 | 241.11 | -7.13% | 274.62 | 250.20 | 5.5% | 3.6% | 0.0685 | 0.0122 | 38.13 | 24.12 | 0.08% | 187500 |
| examples | 4255493 | 230.88 | 211.12 | -8.56% | 241.21 | 216.48 | 4.3% | 2.5% | 0.0752 | 0.0217 | 34.57 | 20.79 | 0.05% | 187500 |
| syntax (all 119) | 12490163 | 79.23 | 80.75 | +1.92% | 84.22 | 84.06 | 5.9% | 3.9% | 0.0048 | 0.0038 | 27.66 | 22.11 | 0.17% | 207062 |
| syntax (median target) | 0 | 84.41 | 84.66 | +0.30% | 85.12 | 85.32 | 0.8% | 0.8% | 0.0089 | 0.0070 | 29.40 | 22.81 | 0.48% | - |
| internal/sketch/std | 6236077 | 72.97 | 74.47 | +2.05% | 77.93 | 77.21 | 6.4% | 3.6% | 0.0032 | 0.0027 | 24.49 | 19.79 | 0.15% | 207062 |
| compiler | 1998593 | 89.29 | 92.61 | +3.72% | 95.53 | 99.14 | 6.5% | 6.6% | 0.0030 | 0.0024 | 33.28 | 27.30 | 0.16% | 207062 |
| examples | 4255493 | 83.53 | 84.38 | +1.02% | 87.45 | 87.55 | 4.5% | 3.6% | 0.0079 | 0.0062 | 29.62 | 23.08 | 0.19% | 207062 |
| ir-write | 11097084 | 54.08 | 49.15 | -9.12% | 58.25 | 51.56 | 7.2% | 4.7% | 0.0612 | 0.0313 | 9.19 | 7.70 | 0.14% | 124742 |
| checkdump | 630656 | 3763.56 | 3739.84 | -0.63% | 5393.09 | 5254.78 | 30.2% | 28.8% | - | - | - | - | 0.12% | 241320 |
| irdump | 630656 | 14841.71 | 14555.99 | -1.93% | 30836.70 | 30214.94 | 51.9% | 51.8% | - | - | - | - | 0.61% | 2073802 |
| nativebuild | 1149 | 26125.15 | 25157.98 | -3.70% | 49384.80 | 38634.89 | 47.1% | 34.9% | - | - | - | - | 0.90% | 95100 |

## Format Targets (Per Target)

```
target                              files     bytes       fmt    fmt-gc     gc%  allocs/B   bytes/B
internal/sketch/std                   561   6236077    179.63    190.57    5.7%    0.0118     18.85
compiler                              171   1998593    241.11    250.20    3.6%    0.0122     24.12
examples/address-routes                 9     37754    212.70    213.18    0.2%    0.0173     21.22
examples/admit                          9     22085    197.45    197.99    0.3%    0.0300     20.82
examples/adventure                     10     31961    214.47    214.81    0.2%    0.0206     22.21
examples/backups                       15     47441    210.05    210.34    0.1%    0.0227     20.64
examples/board                          2     12734    184.68    185.31    0.3%    0.0090     21.09
examples/bookmarks                      5     24247    214.48    214.65    0.1%    0.0202     21.75
examples/bucketstore                    3      5373    252.39    253.98    0.6%    0.0409     25.00
examples/calc                          17     47999    222.71    221.61   -0.5%    0.0254     22.62
examples/canvas-scenes                 10     60647    234.01    234.26    0.1%    0.0203     22.19
examples/catalog                        4     19858    208.85    209.14    0.1%    0.0231     20.43
examples/cborpack                       5     13572    201.28    201.47    0.1%    0.0337     20.79
examples/cells                         11     91114    239.57    238.89   -0.3%    0.0177     23.17
examples/chat                           8     37412    187.49    188.00    0.3%    0.0198     19.12
examples/clientgen                      1      7076    179.60    179.61    0.0%    0.0129     18.43
examples/confparse                     13     52535    216.34    216.55    0.1%    0.0228     21.63
examples/counter                        3      1777    245.83    259.01    5.1%    0.1283     25.38
examples/counter-gui                    5     12053    220.81    220.92    0.0%    0.0298     21.41
examples/counter-list                   8     21218    213.44    214.06    0.3%    0.0312     20.64
examples/cron                           6     19327    196.30    196.78    0.2%    0.0229     19.40
examples/csvreport                     10     28515    192.18    192.65    0.2%    0.0253     19.35
examples/dash-gui                      14     51916    212.48    212.12   -0.2%    0.0209     20.89
examples/deploys                       17     58937    189.96    190.55    0.3%    0.0230     19.53
examples/deps                          12     54760    212.37    212.67    0.1%    0.0200     21.36
examples/domain-search                 24    150131    198.47    197.95   -0.3%    0.0153     19.01
examples/download                       4      8351    206.31    207.25    0.5%    0.0299     20.60
examples/downloads                      5     30287    196.35    196.81    0.2%    0.0182     19.75
examples/draw-gui                      19     70517    243.45    242.47   -0.4%    0.0294     23.55
examples/echo-chat                      7     33670    196.03    196.19    0.1%    0.0233     19.68
examples/editions                       1       706    235.84    238.02    0.9%    0.0765     27.13
examples/editor-gui                    16     75877    219.60    219.94    0.2%    0.0193     21.01
examples/elevator                       4     14526    225.02    225.35    0.1%    0.0229     22.56
examples/events                         5      6726    209.70    210.52    0.4%    0.0412     20.34
examples/extract                        5     12559    194.27    194.81    0.3%    0.0271     19.63
examples/fetchpool                      4     12015    236.71    236.81    0.0%    0.0304     23.86
examples/fieldnotes                     9     29570    204.74    204.97    0.1%    0.0234     19.92
examples/flattree                      17     60145    233.69    234.14    0.2%    0.0230     24.03
examples/fluid-gallery                 10     45416    249.64    253.60    1.6%    0.0204     24.23
examples/forge                         11     43661    206.24    206.55    0.2%    0.0218     20.32
examples/gallery                       10     43681    210.47    209.12   -0.6%    0.0190     20.42
examples/gent                          14     98835    194.80    194.79   -0.0%    0.0178     19.08
examples/gointerop                      9     21217    195.34    195.94    0.3%    0.0297     19.32
examples/idle-timer                     9     29009    185.49    185.79    0.2%    0.0225     18.91
examples/inbox                          2      6878    235.81    236.92    0.5%    0.0221     21.77
examples/jobq                          12     48553    225.14    225.19    0.0%    0.0215     21.49
examples/jobs                          13     45475    209.97    210.42    0.2%    0.0286     20.62
examples/jobs-web                      12     53216    203.08    203.11    0.0%    0.0212     20.91
examples/jsonparse                     19     54923    226.62    227.06    0.2%    0.0239     22.81
examples/jsonv                         15     51426    204.36    203.12   -0.6%    0.0247     20.04
examples/keypad                        11     33159    197.56    198.12    0.3%    0.0224     19.61
examples/kmeans                        11     25358    212.72    212.87    0.1%    0.0279     20.27
examples/kvstore                       17     52975    195.17    195.74    0.3%    0.0276     20.05
examples/lease-sync                    20    113116    211.21    210.57   -0.3%    0.0191     20.10
examples/limiter                       13     65269    199.13    199.20    0.0%    0.0190     20.26
examples/lint-policy                    4      9235    173.37    174.27    0.5%    0.0228     17.40
examples/logstat                       14     39635    209.82    210.18    0.2%    0.0256     20.75
examples/lookup                         4      3281    193.02    195.26    1.1%    0.0677     21.22
examples/lru                            8     33571    241.88    242.21    0.1%    0.0242     24.58
examples/lzpack                        11     29226    208.97    209.32    0.2%    0.0250     20.87
examples/markup                         2      3427    245.17    246.79    0.7%    0.0406     21.86
examples/mdhtml                        10    115734    207.10    209.18    1.0%    0.0125     20.27
examples/metrics                        7     26495    224.00    223.68   -0.1%    0.0232     21.07
examples/model-paths                   10     59233    223.87    222.76   -0.5%    0.0119     21.47
examples/orchestrate                    2     11012    155.86    156.12    0.2%    0.0094     17.04
examples/pager                          2      3546    247.07    248.44    0.5%    0.0460     22.47
examples/pairing                        7     27956    207.14    207.51    0.2%    0.0229     20.16
examples/particles                     16     42272    225.35    225.76    0.2%    0.0364     22.15
examples/permit-wizard                  7     35005    197.31    197.49    0.1%    0.0152     19.19
examples/pipeline                      32    198224    194.79    194.48   -0.2%    0.0171     19.30
examples/pure-eval                      3      8086    193.24    194.09    0.4%    0.0190     19.04
examples/query-table                   11     55339    212.49    211.76   -0.3%    0.0157     19.94
examples/quiz                           9     28143    190.99    191.33    0.2%    0.0210     19.15
examples/quota                         14     45297    200.05    200.31    0.1%    0.0240     20.13
examples/raytrace                       8     28798    259.82    259.93    0.0%    0.0359     25.13
examples/regex                         19    102587    204.68    204.10   -0.3%    0.0173     20.15
examples/relay                          5      4598    197.56    199.01    0.7%    0.0650     20.82
examples/respd                         13     62797    211.98    211.99    0.0%    0.0190     20.77
examples/ritmo                         11     43092    207.21    207.95    0.4%    0.0252     19.66
examples/routecli                       3      3446    247.87    249.99    0.8%    0.0464     25.96
examples/router                         8     61372    197.78    197.58   -0.1%    0.0146     19.85
examples/router-model                   3     11457    185.80    186.40    0.3%    0.0154     19.37
examples/runtime-faults                 6     32380    198.34    198.69    0.2%    0.0170     20.00
examples/scoped-engine                  9     27802    198.17    198.51    0.2%    0.0285     19.57
examples/scopes                         2      5188    234.32    235.08    0.3%    0.0301     22.59
examples/search                         5     22647    200.34    200.78    0.2%    0.0213     19.72
examples/shards                         8     25316    219.72    220.19    0.2%    0.0246     21.48
examples/signup-form                    9     28864    185.22    185.75    0.3%    0.0219     18.48
examples/snake                          8     25599    219.18    219.56    0.2%    0.0266     21.86
examples/sorted-insert                  3      2799    220.07    222.21    1.0%    0.0547     21.47
examples/ssg                           13     41748    195.13    194.79   -0.2%    0.0230     19.26
examples/stack-builder                  8     25313    241.03    240.89   -0.1%    0.0321     24.67
examples/stackvm                       11     58879    227.32    227.74    0.2%    0.0162     21.86
examples/stars-gui                     16     62523    206.08    206.41    0.2%    0.0188     20.16
examples/starter                        4      6759    204.42    205.02    0.3%    0.0339     19.76
examples/station-stats                 10     24405    205.75    205.98    0.1%    0.0313     19.74
examples/stopwatch                      7     28197    189.88    187.70   -1.2%    0.0214     18.18
examples/sudoku                         9     25303    194.65    195.30    0.3%    0.0253     19.40
examples/tally-board                    2      9954    228.15    228.07   -0.0%    0.0133     21.80
examples/task-board                    12     72023    218.24    217.96   -0.1%    0.0194     20.11
examples/tasks-cli                     17     37921    199.71    200.59    0.4%    0.0293     19.58
examples/tidewalk-replay               11     14474    221.60    222.20    0.3%    0.0428     22.26
examples/timetable                      8     46339    215.80    215.55   -0.1%    0.0153     20.49
examples/tn1                            2     10639    223.06    223.03   -0.0%    0.0184     20.96
examples/tn1-lint                       2      8545    261.22    261.32    0.0%    0.0296     27.57
examples/todo-gui                       5     15856    224.39    224.33   -0.0%    0.0223     21.34
examples/todo-shell                    10     23695    204.90    205.28    0.2%    0.0298     21.16
examples/toktree                        3     12683    250.17    250.47    0.1%    0.0307     23.94
examples/tomlparse                     13     52752    233.85    234.24    0.2%    0.0201     23.16
examples/transfers                     11     45516    203.68    203.95    0.1%    0.0286     19.78
examples/triage                         5     11880    193.68    194.24    0.3%    0.0255     18.75
examples/tui                            8     21290    205.27    205.59    0.2%    0.0275     19.95
examples/twig                          18     77246    242.50    242.41   -0.0%    0.0194     24.74
examples/uitree                         2      4065    224.48    225.68    0.5%    0.0312     21.42
examples/vt                            20    106608    220.54    219.96   -0.3%    0.0207     21.67
examples/weather                       10     35504    181.18    181.37    0.1%    0.0256     18.48
examples/wire-config                    2       920    252.91    255.54    1.0%    0.0837     23.97
examples/work                           1      6187    163.96    164.15    0.1%    0.0170     17.31
examples/wtcache                        5      7252    203.52    205.30    0.9%    0.0552     21.22
```
