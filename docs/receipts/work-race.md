# Receipt: a remove racing a create on it, `work` against Rift

SEMWS WG1, 2026-11-15. The registry of `work` (plan §2.12, `modules/work`) against Rift 0.0.13 on the race that left Rift's registry with folders no record names: in each trial, one process removes a workspace while another creates a workspace on top of it, both at once.

How: `scripts/workrace/workrace.sh 300` with `S` a scratch folder on btrfs (the base is a subvolume, so each clone is a snapshot), started at a 1-minute load under 48 (47.9), every `work` call inside `systemd-run --user --scope -q -p MemoryMax=8G -p MemorySwapMax=0 -- nice -n 19`, each call after a random pause under 120 ms. Trial i: `work new a<i>`, then at once `work rm -abandon -expect ID a<i>` and `work new -on a<i> b<i>`. The script counts each call's status, both folders and both records, then the folders under the storage that no record names. Rift's run is the SEMWS r2 research run of the same race (`rift create --copy-all --no-hooks` racing `rift remove`, 300 trials, 2026-11-15T03:20Z, load 67).

| Outcome (300 trials each) | Rift 0.0.13 | `work` |
| --- | ---: | ---: |
| remove won, the create refused (`work`: `NoWorkspace`) | 160 + 108 with a stray source folder | 280 (277 at the WG1 r3 head) |
| the create won, the remove refused (`HasDescendants`) | 0 | 20 (23) |
| both said they succeeded | 32 (a folder with no record) | 0 |
| folders under the storage no record names | 140 | 0 |
| records whose folder is missing | n/a | 0 |

Rift's 108 stray folders are the source's `.git/info/exclude`, which Rift's create wrote into the folder its remove had just taken; its 32 double successes left the new workspace's folder with no record. `work` ends every trial in one of the two serial outcomes the model allows (`models/registry`): the remove first (the create is `NoWorkspace`) or the create first (the remove is `HasDescendants`, the reservation counts as a child), and every folder is a record's.

One defect the first run found, fixed before this run: `new -on` reads its parent's branch with no lock, and when the remove moved the parent in that window the create failed with Git's error (`VcsFailed`, 20 of 300) instead of `NoWorkspace`. It left no folder; the answer is now `NoWorkspace` when the parent is gone.

Raw rows: the unit's run folder (`semws/WG1/race-300.txt`, and `race-300-r3.txt` at the WG1 r3 head, after Claim compares the parent's id: 0 orphans, 0 records without a folder; Rift's in `semws/receipts/race-create-remove.txt`); `scripts/workrace/workrace.sh N` rewrites them.
