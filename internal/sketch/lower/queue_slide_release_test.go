package lower

import "testing"

// TestSlideReleasesTheEvicted (SLIDE-RELEASE): Slide gives the evicted
// value back, the caller binds it (resources R42), and its scope releases
// it, so every resource made is closed once: the evicted one by its
// binding, the one left by the queue's release (last bound, first
// released). Mutant only it kills (testdata/mutants.json):
// slide-lowered-evicted-dropped (the (T, bool, error) adaptation gives None
// when ok).
//
// It builds Go because the evicted value's release is the lowered
// program's ownership code, which only a run of the built program shows.
func TestSlideReleasesTheEvicted(t *testing.T) {
	needsBuild(t)
	wantRun(t, `import queue

type Res { id int } implements Closer

func Close(r Res) needs Console { Console.Println("close \(r.id)") }

func main() needs Console {
	if true {
		let q = queue.New[Res](1)
		let first = q.Slide(Res{id: 1}) catch {
		case queue.Closed:
			None
		}
		let evicted = q.Slide(Res{id: 2}) catch {
		case queue.Closed:
			None
		}
		Console.Println("slid \(first == None)")
	}
	Console.Println("end")
}
`, "slid true\nclose 1\nclose 2\nend\n")
}
