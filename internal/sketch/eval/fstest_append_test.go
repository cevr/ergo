package eval

import (
	"fmt"
	"testing"
)

// TestFSMemoryAppendCopiesLinearBytes guards fstest.Memory's appends
// (FU-FSTEST-APPEND): a log of n appends and one read copies O(n) bytes,
// as a host file's appends do, so a program that keeps a log in a Memory
// (examples/kvstore) runs its laws under the default budget. It counts the
// value bytes the evaluation builds (Report.Bytes), never time, at n and
// at 2n appends: an append that copies the whole file again builds about
// four times the bytes at 2n, a linear one about twice. The read must give
// the lines in the order they were appended, which lower
// TestFSMemoryAgreesWithHost reaches only by chance (its sequences seldom
// append twice to one file before a read). Mutants only it kills
// (testdata/mutants.json, lower's testdata/mutants.json):
// fstest-append-copies-the-file, fstest-read-parts-last-first.
func TestFSMemoryAppendCopiesLinearBytes(t *testing.T) {
	built := func(n int) int64 {
		t.Helper()
		pkg := checked(t, fmt.Sprintf(`import fs
import fstest
import strings

func logged(n int) bool {
	let m = fstest.NewMemory()
	for i in 0..n {
		m.AppendFile("log", "\(i)\n") catch {
		case fs.NotFound, fs.Denied, fs.Failed:
			return false
		}
	}
	let text = m.ReadFile("log") catch {
	case fs.NotFound, fs.Denied, fs.Failed:
		return false
	}
	strings.Count(text, "\n") == n && strings.HasPrefix(text, "0\n1\n2\n") && strings.HasSuffix(text, "\n\(n-1)\n")
}

test Log {
	assert logged(%d)
}
`, n))
		b := DefaultBudget()
		b.Steps = 100_000_000
		rep, err := Run(pkg, b)
		if err != nil {
			t.Fatal(err)
		}
		if len(rep.Results) != 1 || rep.Results[0].Status != Passed {
			t.Fatalf("%d appends: %+v", n, rep.Results)
		}
		return rep.Bytes
	}
	const n = 256
	once, twice := built(n), built(2*n)
	t.Logf("value bytes: %d appends %d, %d appends %d", n, once, 2*n, twice)
	if twice > 3*once {
		t.Errorf("%d appends of a line build %d value bytes, %d appends %d: more than 3 times, so an append copies the file", n, once, 2*n, twice)
	}
}
