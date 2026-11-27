# Compiler DoD Floor Receipt

- Commit: `93bf75ae7`
- Measurement method: `internal/opcount` (`CountThread` in-process per-target + `Count` isolated child processes, `5` rounds alternating `GOGC=off` and `GOGC=100`, median and spread `(max-min)/median`, peak RSS from `VmHWM` / `ru_maxrss`).
- Reference baseline (`docs/receipts/self-host.md` at `e56dafb19`): weighted `fmt 191.5`, `parse 69.5` instr/byte across 98 targets (6,285,816 bytes); median target `fmt 234.6`, `parse 83.8`; `compiler` `fmt 265.39`, `parse 85.70`, `GC 10.8%`, `0.078 allocs/B`, `37.38 bytes/B`.

## Static DoD Invariants (`compiler/**`)

- Files scanned: `171`
- Structs scanned: `533`, fields scanned: `1907`, `Map` types scanned: `148`, functions scanned (`syntax`/`format`/`ir`): `712`
- Findings: **0 violations** (`no-pointer-keyed-maps`: PASS, `dense-uint32-ids`: PASS, `no-per-node-scratch-allocs`: PASS)

## Corpus DoD Summary

| Workload | Units | Base `GOGC=off` | New `GOGC=off` | Delta | Base `GOGC=100` | New `GOGC=100` | Base GC% | New GC% | Base `allocs/B` | New `allocs/B` | Base `bytes/B` | New `bytes/B` | New Spread (`off`) | Peak RSS (KiB) |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| format (all 119) | 12490364 | 217.21 | 200.13 | -7.87% | 229.96 | 207.67 | 5.5% | 3.6% | 0.0643 | 0.0152 | 32.46 | 20.35 | 0.07% | 187634 |
| format (median target) | 0 | 229.77 | 209.68 | -8.74% | 233.28 | 210.52 | 1.5% | 0.4% | 0.0765 | 0.0230 | 34.69 | 20.64 | 0.22% | - |
| internal/sketch/std | 6236077 | 194.03 | 179.58 | -7.45% | 207.41 | 187.48 | 6.5% | 4.2% | 0.0553 | 0.0118 | 29.16 | 18.85 | 0.07% | 187634 |
| compiler | 1998794 | 259.62 | 241.02 | -7.16% | 274.62 | 249.73 | 5.5% | 3.5% | 0.0685 | 0.0122 | 38.13 | 24.12 | 0.07% | 187634 |
| examples | 4255493 | 230.88 | 211.04 | -8.60% | 241.21 | 217.06 | 4.3% | 2.8% | 0.0752 | 0.0217 | 34.57 | 20.79 | 0.07% | 187634 |
| syntax (all 119) | 12490364 | 79.23 | 80.83 | +2.02% | 84.22 | 84.43 | 5.9% | 4.3% | 0.0048 | 0.0038 | 27.66 | 22.11 | 0.27% | 196984 |
| syntax (median target) | 0 | 84.41 | 84.69 | +0.34% | 85.12 | 85.11 | 0.8% | 0.5% | 0.0089 | 0.0070 | 29.40 | 22.81 | 0.40% | - |
| internal/sketch/std | 6236077 | 72.97 | 74.54 | +2.15% | 77.93 | 77.90 | 6.4% | 4.3% | 0.0032 | 0.0027 | 24.49 | 19.79 | 0.28% | 196984 |
| compiler | 1998794 | 89.29 | 92.66 | +3.77% | 95.53 | 96.61 | 6.5% | 4.1% | 0.0030 | 0.0024 | 33.28 | 27.30 | 0.30% | 196984 |
| examples | 4255493 | 83.53 | 84.47 | +1.13% | 87.45 | 87.30 | 4.5% | 3.2% | 0.0079 | 0.0062 | 29.62 | 23.08 | 0.24% | 196984 |
| ir-write | 11097084 | 54.08 | 49.15 | -9.11% | 58.25 | 52.15 | 7.2% | 5.7% | 0.0612 | 0.0313 | 9.19 | 7.70 | 0.10% | 119510 |
| checkdump | 630656 | 3763.56 | 3741.54 | -0.58% | 5393.09 | 5307.55 | 30.2% | 29.5% | - | - | - | - | 0.12% | 245838 |
| irdump | 630656 | 14841.71 | 14576.02 | -1.79% | 30836.70 | 30106.26 | 51.9% | 51.6% | - | - | - | - | 0.76% | 2087306 |
| nativebuild | 1149 | 26125.15 | 25218.59 | -3.47% | 49384.80 | 38638.41 | 47.1% | 34.7% | - | - | - | - | 0.87% | 104324 |

## Format Targets (Per Target)

```
target                              files     bytes       fmt    fmt-gc     gc%  allocs/B   bytes/B
internal/sketch/std                   561   6236077    179.58    187.48    4.2%    0.0118     18.85
compiler                              171   1998794    241.02    249.73    3.5%    0.0122     24.12
examples/address-routes                 9     37754    212.80    213.17    0.2%    0.0173     21.22
examples/admit                          9     22085    197.32    198.08    0.4%    0.0300     20.82
examples/adventure                     10     31961    214.51    214.89    0.2%    0.0206     22.21
examples/backups                       15     47441    210.55    210.13   -0.2%    0.0227     20.64
examples/board                          2     12734    184.58    185.36    0.4%    0.0090     21.09
examples/bookmarks                      5     24247    214.44    214.68    0.1%    0.0202     21.75
examples/bucketstore                    3      5373    252.81    253.71    0.4%    0.0409     25.00
examples/calc                          17     47999    221.16    221.54    0.2%    0.0254     22.62
examples/canvas-scenes                 10     60647    233.97    233.99    0.0%    0.0203     22.19
examples/catalog                        4     19858    208.93    209.20    0.1%    0.0231     20.43
examples/cborpack                       5     13572    201.17    201.71    0.3%    0.0337     20.79
examples/cells                         11     91114    238.85    239.40    0.2%    0.0177     23.17
examples/chat                           8     37412    187.57    187.84    0.1%    0.0198     19.12
examples/clientgen                      1      7076    179.59    179.48   -0.1%    0.0129     18.43
examples/confparse                     13     52535    216.16    216.51    0.2%    0.0228     21.63
examples/counter                        3      1777    246.14    246.09   -0.0%    0.1283     25.38
examples/counter-gui                    5     12053    220.48    220.91    0.2%    0.0298     21.41
examples/counter-list                   8     21218    213.49    213.96    0.2%    0.0312     20.64
examples/cron                           6     19327    196.24    196.59    0.2%    0.0229     19.40
examples/csvreport                     10     28515    192.46    192.62    0.1%    0.0253     19.35
examples/dash-gui                      14     51916    211.82    212.05    0.1%    0.0209     20.89
examples/deploys                       17     58937    189.95    190.40    0.2%    0.0230     19.53
examples/deps                          12     54760    212.47    212.72    0.1%    0.0200     21.36
examples/domain-search                 24    150131    198.45    198.01   -0.2%    0.0153     19.01
examples/download                       4      8351    206.45    207.75    0.6%    0.0299     20.60
examples/downloads                      5     30287    196.42    196.72    0.2%    0.0182     19.75
examples/draw-gui                      19     70517    242.20    242.45    0.1%    0.0294     23.55
examples/echo-chat                      7     33670    195.80    196.30    0.3%    0.0233     19.68
examples/editions                       1       706    236.81    238.02    0.5%    0.0765     27.13
examples/editor-gui                    16     75877    220.56    220.40   -0.1%    0.0193     21.01
examples/elevator                       4     14526    224.82    225.49    0.3%    0.0229     22.56
examples/events                         5      6726    210.57    210.13   -0.2%    0.0412     20.34
examples/extract                        5     12559    194.34    194.59    0.1%    0.0271     19.63
examples/fetchpool                      4     12015    236.24    236.89    0.3%    0.0304     23.86
examples/fieldnotes                     9     29570    204.91    204.88   -0.0%    0.0234     19.92
examples/flattree                      17     60145    233.49    233.69    0.1%    0.0230     24.03
examples/fluid-gallery                 10     45416    249.76    253.55    1.5%    0.0204     24.23
examples/forge                         11     43661    206.34    206.57    0.1%    0.0218     20.32
examples/gallery                       10     43681    209.22    209.18   -0.0%    0.0190     20.42
examples/gent                          14     98835    194.53    194.61    0.0%    0.0178     19.08
examples/gointerop                      9     21217    195.08    195.70    0.3%    0.0297     19.32
examples/idle-timer                     9     29009    185.64    186.00    0.2%    0.0225     18.91
examples/inbox                          2      6878    236.41    236.97    0.2%    0.0221     21.77
examples/jobq                          12     48553    224.99    225.17    0.1%    0.0215     21.49
examples/jobs                          13     45475    209.90    210.27    0.2%    0.0286     20.62
examples/jobs-web                      12     53216    202.80    203.40    0.3%    0.0212     20.91
examples/jsonparse                     19     54923    226.81    227.03    0.1%    0.0239     22.81
examples/jsonv                         15     51426    202.66    203.03    0.2%    0.0247     20.04
examples/keypad                        11     33159    197.58    197.77    0.1%    0.0224     19.61
examples/kmeans                        11     25358    212.59    212.74    0.1%    0.0279     20.27
examples/kvstore                       17     52975    195.28    195.76    0.2%    0.0276     20.05
examples/lease-sync                    20    113116    211.07    210.65   -0.2%    0.0191     20.10
examples/limiter                       13     65269    199.09    199.34    0.1%    0.0190     20.26
examples/lint-policy                    4      9235    173.40    173.95    0.3%    0.0228     17.40
examples/logstat                       14     39635    209.71    210.29    0.3%    0.0256     20.75
examples/lookup                         4      3281    194.04    194.62    0.3%    0.0677     21.22
examples/lru                            8     33571    242.09    242.33    0.1%    0.0242     24.58
examples/lzpack                        11     29226    209.08    209.43    0.2%    0.0250     20.87
examples/markup                         2      3427    244.99    246.13    0.5%    0.0406     21.86
examples/mdhtml                        10    115734    207.38    208.98    0.8%    0.0125     20.27
examples/metrics                        7     26495    223.33    223.49    0.1%    0.0232     21.07
examples/model-paths                   10     59233    222.82    222.93    0.0%    0.0119     21.47
examples/orchestrate                    2     11012    155.81    156.21    0.3%    0.0094     17.04
examples/pager                          2      3546    245.81    248.16    0.9%    0.0460     22.47
examples/pairing                        7     27956    207.45    207.28   -0.1%    0.0229     20.16
examples/particles                     16     42272    225.99    225.70   -0.1%    0.0364     22.15
examples/permit-wizard                  7     35005    197.39    197.54    0.1%    0.0152     19.19
examples/pipeline                      32    198224    194.66    194.40   -0.1%    0.0171     19.30
examples/pure-eval                      3      8086    193.03    193.41    0.2%    0.0190     19.04
examples/query-table                   11     55339    211.44    211.60    0.1%    0.0157     19.94
examples/quiz                           9     28143    190.81    191.10    0.2%    0.0210     19.15
examples/quota                         14     45297    200.00    200.27    0.1%    0.0240     20.13
examples/raytrace                       8     28798    259.63    260.05    0.2%    0.0359     25.13
examples/regex                         19    102587    204.92    204.29   -0.3%    0.0173     20.15
examples/relay                          5      4598    196.87    198.30    0.7%    0.0650     20.82
examples/respd                         13     62797    211.54    211.84    0.1%    0.0190     20.77
examples/ritmo                         11     43092    207.23    207.60    0.2%    0.0252     19.66
examples/routecli                       3      3446    248.35    250.59    0.9%    0.0464     25.96
examples/router                         8     61372    197.58    197.60    0.0%    0.0146     19.85
examples/router-model                   3     11457    186.18    186.38    0.1%    0.0154     19.37
examples/runtime-faults                 6     32380    198.24    198.83    0.3%    0.0170     20.00
examples/scoped-engine                  9     27802    198.00    198.47    0.2%    0.0285     19.57
examples/scopes                         2      5188    234.80    234.87    0.0%    0.0301     22.59
examples/search                         5     22647    200.41    200.79    0.2%    0.0213     19.72
examples/shards                         8     25316    219.61    220.26    0.3%    0.0246     21.48
examples/signup-form                    9     28864    185.27    185.78    0.3%    0.0219     18.48
examples/snake                          8     25599    219.10    219.63    0.2%    0.0266     21.86
examples/sorted-insert                  3      2799    218.90    221.14    1.0%    0.0547     21.47
examples/ssg                           13     41748    194.36    194.71    0.2%    0.0230     19.26
examples/stack-builder                  8     25313    240.92    240.98    0.0%    0.0321     24.67
examples/stackvm                       11     58879    227.41    227.40   -0.0%    0.0162     21.86
examples/stars-gui                     16     62523    206.19    206.50    0.2%    0.0188     20.16
examples/starter                        4      6759    204.52    205.70    0.6%    0.0339     19.76
examples/station-stats                 10     24405    205.38    205.88    0.2%    0.0313     19.74
examples/stopwatch                      7     28197    187.51    187.94    0.2%    0.0214     18.18
examples/sudoku                         9     25303    194.72    195.07    0.2%    0.0253     19.40
examples/tally-board                    2      9954    227.70    227.85    0.1%    0.0133     21.80
examples/task-board                    12     72023    217.99    218.00    0.0%    0.0194     20.11
examples/tasks-cli                     17     37921    199.83    200.39    0.3%    0.0293     19.58
examples/tidewalk-replay               11     14474    221.35    222.47    0.5%    0.0428     22.26
examples/timetable                      8     46339    215.57    215.67    0.0%    0.0153     20.49
examples/tn1                            2     10639    222.14    222.76    0.3%    0.0184     20.96
examples/tn1-lint                       2      8545    261.45    261.49    0.0%    0.0296     27.57
examples/todo-gui                       5     15856    224.21    224.52    0.1%    0.0223     21.34
examples/todo-shell                    10     23695    204.56    205.37    0.4%    0.0298     21.16
examples/toktree                        3     12683    250.31    250.76    0.2%    0.0307     23.94
examples/tomlparse                     13     52752    233.88    234.21    0.1%    0.0201     23.16
examples/transfers                     11     45516    203.55    204.01    0.2%    0.0286     19.78
examples/triage                         5     11880    193.92    194.41    0.3%    0.0255     18.75
examples/tui                            8     21290    205.13    205.45    0.2%    0.0275     19.95
examples/twig                          18     77246    242.57    242.37   -0.1%    0.0194     24.74
examples/uitree                         2      4065    224.48    225.14    0.3%    0.0312     21.42
examples/vt                            20    106608    220.37    243.34    9.4%    0.0207     21.67
examples/weather                       10     35504    181.17    181.39    0.1%    0.0256     18.48
examples/wire-config                    2       920    252.91    255.98    1.2%    0.0837     23.97
examples/work                           1      6187    163.85    163.84   -0.0%    0.0170     17.31
examples/wtcache                        5      7252    203.44    205.29    0.9%    0.0552     21.22
```
