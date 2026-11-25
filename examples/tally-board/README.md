# tally-board

A small tally board UI example demonstrating autonomous UI exploration with
`ui.Offers`, `ui.Explore`, and `ui.Defaults`.

- `board/board.ergo` defines the `Board` state machine, its `View` (with item
  creation up to `MaxItems`, detail navigation + `GoBack` history, and an async
  `ui.Load` sync), and `law Explored` which runs `ui.Explore` across seeds 1 to 3
  checking `ui.Defaults()` alongside the board's invariants.
- `cmd/tally-board/main.ergo` runs a bounded exploration walk and prints its
  coverage summary.
- `testdata/mutants.json` plants four defects caught by `law Explored`:
  `tally-count-past-limit`, `tally-load-never-settles`,
  `tally-duplicate-button-name`, and `tally-back-after-delete-defect`.
