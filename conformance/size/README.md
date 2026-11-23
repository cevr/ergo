# Size conformance

Raw measurement records for the [small executable contract](../../docs/specs/binary-reachability.md). Every number here is a raw measurement. None is a performance claim or a size budget.

The .ef migration removed the earlier `.ef` language, and with it the size fixtures, the size matrix and `ergo build --receipt` ([retirement record](../../docs/receipts/retire-ef.md)). Nothing in this directory runs now. The fixtures return in the decided syntax with v1 item 5, which measures build size and time again.

| Path | Contents |
| --- | --- |
| `controls/go/minimal`, `controls/go/managed` | Matched idiomatic Go controls that keep a fixture's contract with the standard library only. The minimal control cancels on a termination signal through `signal.NotifyContext` and runs its work in an owning scope. They compile with the other Go packages; nothing measures them. |
| `controls/go/minimal-floor` | Unmatched floor: the minimal result printed with no cancellation or scope. It bounded the minimal row from below and was never compared as an equivalent program. |
| `receipts/*.json` | Recorded matrix runs. Each one is bound to its commit, dirty state, toolchain and fixture, control and runtime hashes. Runs recorded before 2026-10-26 (schema `ergo.size-conformance/2`) also measured the removed JavaScript target and TypeScript controls. |

The removed fixtures, matrix and receipt code are at commit `dbf135bf`: [`conformance/size/fixtures`](https://github.com/cevr/ergo/blob/dbf135bff09901e85385945593113f35b300199c/conformance/size/fixtures), [`conformance/size/matrix`](https://github.com/cevr/ergo/blob/dbf135bff09901e85385945593113f35b300199c/conformance/size/matrix) and [`internal/receipt`](https://github.com/cevr/ergo/blob/dbf135bff09901e85385945593113f35b300199c/internal/receipt/receipt.go).
