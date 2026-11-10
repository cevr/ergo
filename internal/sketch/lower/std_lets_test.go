package lower

import "testing"

// TestStdGenericLetsCompile is the F1 repro: an embedded std body reads a
// package let, including through a generic function and tuple binding.
// The std-let-tuple-location mutant gives an embedded tuple initializer
// a user-file location. Existing arena laws use only scalar std lets and
// cannot catch it; unlike user source, std has no Unit.Imports file.
func TestStdGenericLetsCompile(t *testing.T) {
	lib := stdFixture(t, "letfixture", `let none = -1
let left, right = pair()
func pair() (int, int) { (2, 3) }
func Read[T](x T) (T, int) { (x, none + left + right) }
`)
	u := checkPackage(t, "main", []pkgSource{{"/ergo-test/stdlets/main.ergo", `import letfixture
func main() { let _ = letfixture.Read(7) }
`}}, pkgImporter{"letfixture": lib})
	typecheckGo(t, []*lowered{mustLowerUnit(t, "stdlets", u, false)})
}
