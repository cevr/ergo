package v1

// heapFloorBytes is the heap floor of a program (runtime.md, Entry 6): Main
// keeps a ballast of this many bytes live, so the collector's goal is
// twice the live heap plus it, never Go's own 4 MiB minimum. A server's
// live heap is small (examples/jobs keeps under 1 MiB), so at the minimum
// goal it collected after every 3 MiB of garbage: 8.7 cycles per 1,000
// requests of a create/read/cancel/delete mix, 40 of a 100-job listing.
// The ballast is a byte array the collector never scans, and its pages are
// never written, so the host does not map them: it costs no CPU and no
// resident memory of its own. The garbage room it adds (about this much)
// is the memory it costs: with it the two mixes collect 10× and 5× less
// often, for 15 MB and 8 MB more peak resident memory.
//
// A program whose environment sets GOGC or GOMEMLIMIT has chosen its own
// collector policy, and gets no ballast: Go's settings apply as written,
// and a memory limit is not spent on a ballast.
//
// Prior art: Go's GC guide sizes the goal from GOGC and GOMEMLIMIT and
// keeps a 4 MiB minimum; Twitch's and Uber's servers kept a heap ballast
// or tuned GOGC from the live heap for the same reason; the JVM and V8
// (behind Bun's and Node's servers) start with a young generation of
// several to tens of MiB, so a small server collects rarely there too.
const heapFloorBytes = 16 << 20

// ballast is the heap floor's ballast, live for the life of the program.
var ballast []byte

// heapFloor installs the heap floor unless getenv names a GOGC or a
// GOMEMLIMIT, and reports whether it did.
func heapFloor(getenv func(string) string) bool {
	if getenv("GOGC") != "" || getenv("GOMEMLIMIT") != "" || ballast != nil {
		return false
	}
	ballast = make([]byte, heapFloorBytes)
	return true
}
