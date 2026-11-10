# E3b state

Branch wf10/e3b, base 47eb057a. Round 1, share A1.
Code head 719a52ba68f9220e6d86839c1433e89658652e54.

Commits: 40a1aed0 red acceptance; be914b5f failure table/projection/std helper;
9a17b931 formatted law ledger; 1656a881 precise rows and HEAD refusal;
719a52ba law layout and checked rename. Never rebased or pushed.

Done: mapper and server read the same failure table; stock client receives
tagged typed failures; negative call sites detect renamed/unrelated failures;
std and example laws, diagnostics, mutant ledgers and docs are updated.
Measured: red-1.log exit 1; accept-3.log exit 0; gate-1.log exit 0;
mutants-2.log exit 0 (21 killed, with explicit FailuresCoverContract
refutations). The source was restored and the owned process scan was clean.

Next: commit request, post-artifact residue check, request validator and private
cache cleanup, then READY. Detailed live state and logs are in the host's
/home/exedev/.cache/baton/wf10/mapper-gaps/E3b directory.
Open questions: none.

Decisions: one ordinary exported literal []http.FailureStatus table per
package; Tag names codec failure tags and mapper checks them against nominal
contract failures. Decided by derive-dont-sync and explicit-contract principles.
The private carrier names an anonymous cross-package failure row because an
ordinary union cannot include foreign variants. Decided by boundary-discipline:
do not change the compiler's foreign-union rule in this transport unit.
HEAD failures are a projection diagnostic, not an undecodable typed promise.

Follow-ups: no new compiler work in scope; existing cross-file TS watch limits
remain. No speed claim or paid call was made. Batch/full/merge/Mac gates belong
to the orchestrator.
