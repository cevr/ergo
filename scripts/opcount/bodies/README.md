# bodies: the count of an NP4f move

An NP4f unit moves a std function from an extern of the Ergo runtime to an
`import go` call or an Ergo body (np4f-plan §6.1). The move lands only if one
call of the new function costs at most 1.02 times the old body's, in user
instructions per call on the IR path.

`main.ergo` is `bodies KIND N`: N calls of the std function KIND (as
`package.Name`) in one loop, and a checksum. A move unit adds a kind for each
function it moves, before the move.

To count a move:

1. Build ergo at the base (the old extern) and at the head (the move), and
   build `main.ergo` with each (`ergo build`, the product path: IR bodies).
   The two binaries run the same loop; only the std function differs.
2. For each kind, run `go run ./scripts/opcount -cmd -base BASE -head HEAD
   -rounds 5 -o FILE -- KIND N` at two sizes N1 < N2, at load < 48.
3. Per call is the slope: (estimate at N2 - estimate at N1) / (N2 - N1), per
   side, so start-up cancels; the ratio is head over base.
4. Both binaries print the same checksum for each kind, or the count is
   refused.

The receipt of each move lists kind, old and new instructions per call, the
ratio and the load. A kind over 1.02 is not moved: its row in
`internal/sketch/std/testdata/std-go-share.txt` keeps the extern and names the capability it waits on.
