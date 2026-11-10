implementor G3 r1: blocked /home/exedev/.cache/baton/wf10/g3-json/G3/blocked.md

Protocol: supplied implementor worker protocol. Spec: `/home/exedev/.cache/baton/wf10/g3-json/brief.md`, G3. Head: `9ccdd3189864df18707300b948e0fa1733a20f74`, branch `wf10/g3`.

## Blocker

Measured: the required closure sweep is red. The shared run's `g3-json/gate-1.log` ends `gate exit 1`. `TestRuntime199MaskProperty` fails with `new call kind 9 needs its Close dispatch path`. The base's `internal/sketch/lower/runtime199_mask_test.go`, `closeDispatch`, also omits codec call kinds. G3 adds no call kind. Other sweep shards time out. No G3 child remains in the process listing.

## Receipts

Measured: gofmt, vet and short tests of check/lower/std/runtime/v1 pass. `TestJSONFieldsAgree`, `TestStdJSONLawsRun`, `TestStdCodecLawsRun`, residue and the gate cache guard pass. New acceptance was red before implementation (missing wrapper types) and is green at this head. Umask 022, GOMAXPROCS=1, go -p 2, one private cache.

## Recommendation

Supply a prerequisite fix that classifies `CallEncode`/`CallDecode` in the existing Close-dispatch fixture, or authorize this lane to fix that fixture separately. Then rerun the closure sweep with `ERGO_CLOSURE_JOBS=1` and a sufficient watchdog.

## Remaining

G3 is not READY: judge the new ledger mutants, guard and complete contextual field witnesses for Optional used as a generic record-field argument, add a matched wrapper benchmark/bench-pending command, and finish the final request. G4 and the jsonv migration have not started. The shared run's blocked report has the complete receipt and options.
