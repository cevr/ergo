ready

Protocol: ~/.claude/skills/orchestrate/protocol.md (supplied worker protocol, implementor).
Spec: /home/exedev/.cache/baton/wf10/gaps-1/gap-wire/spec.md; ../brief.md item 4 and ../decisions.md option 1.
Head: 66ca3a3d95f5702571508dfbb7801add1a2757c9 (implementation; wf10/gap-wire).

Base: b6113200 (gap-group). `git diff --stat b6113200..66ca3a3d | tail -1`: 35 files changed, 383 insertions(+), 62 deletions(-), measured. This committed request mirrors the run-directory request at /home/exedev/.cache/baton/wf10/gaps-1/gap-wire/round-1-request.md, which holds the full by-file report, commands, evidence and known gaps.

Implemented: optional per-field key literal, one checked origin key shared by JSON/TOML, RPC paths/digest and the TypeScript mapper. Effect 4.1.2 uses Schema.encodeKeys (its fromKey equivalent). Inclusion and instantiation preserve the key. Invalid keys, collisions and discriminator collisions are refused. The seeded mapper property covers renamed keys, imported/variant/error keys, colliding defaults and JS escapes. The neutral wire-config example states the cross-format law.

Round gate: `zsh /tmp/opencode/gaps1/wire-gate.sh`; /home/exedev/.cache/baton/wf10/gaps-1/gap-wire/gate-1.log, `EXIT 1` from measured baseline failures only after this unit's guard repairs. All unit acceptance checks exit 0: TestWireKeysDeclared/TestWireKeyDiagnostics/TestWireKeysFollowTheRule; ERGO_TS_TEST=1 TestTsProjectionAgrees (seeds 1 and 7); scripts/examples wire-config. Mutants-1.log EXIT 0: gw-key, gw-origin, gw-invalid, gw-group, gw-format, gw-tag, gw-encode, gw-decode, gw-ts-key, gw-ts-default-clash, gw-rpc-record-key, gw-rpc-variant-key, gw-rpc-digest, gw-rpc-tag, gw-ts-escape and existing wire-key-clash/r3-case all killed by assertions. Red first: red-1.log and red-ts-1.log EXIT 1 at b6113200.

Known gaps: TestStdProtocolSealed and CU7's committed-report residue are red at 50facc08 too (base-red-1.log EXIT 1). They are not changed here. No full gate, paid call or performance claim. Share D, umask 022 and common.md's explicit memory-scope departure used; no owned process remains. Follow up by merging into cu8 with a merge commit, then gap-sched; remove owned scratch base/cache at the lane's final READY.
