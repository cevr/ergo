package lower

import "testing"

// Completing a deferred definition must preserve legal recursion through
// a slice. This exercises the resulting field types in native Go too.
func TestErgoRunE1DefinedRecordIndirection(t *testing.T) {
	needsBuild(t)
	wantRun(t, `type Alias Rec
type Rec { Next []Alias, N int }
func main() needs Console {
	let x = Alias{Next: []Alias{}, N: 7}
	Console.Println("\(x.N)")
}`, "7\n")
}
