package lower

import "testing"

// TestRefinedParseLowersAsFailure is the smallest repro of the L6 bug: a
// refined type of an imported package derives ParsePort, which fails the
// prelude's Unsatisfied, but the lowering gave Unsatisfied no Error method
// and returned it as the Port result, so the Go did not type-check
// (examples/confparse). Mutant: drop the prelude's error-type mark of
// Unsatisfied.
func TestRefinedParseLowersAsFailure(t *testing.T) {
	lib := checkPackage(t, "net", []pkgSource{{"/ergo-test/net/port.ergo", `type Port int where 1 <= it && it <= 65535

func Or(n int, d Port) Port {
	ParsePort(n) catch {
	case Unsatisfied:
		d
	}
}
`}}, nil)
	main := checkPackage(t, "main", []pkgSource{{"/ergo-test/main/main.ergo", `import net

func main() needs Console {
	let p = net.ParsePort(8080) catch {
	case Unsatisfied as u:
		Console.Println(u.Where)
		net.Or(0, 80)
	}
	Console.Println("\(int(p))")
}
`}}, pkgImporter{"net": lib.Package})
	main.Imports = []Unit{lib}
	typecheckGo(t, []*lowered{mustLowerUnit(t, "refined", main, false)})
}
