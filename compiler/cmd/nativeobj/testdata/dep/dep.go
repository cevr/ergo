// Package dep is the stock-compiled dependency the producer's package
// calls (Twice calls Double) and orders its init after (it has an init).
package dep

var inits int

func init() { inits++ }

// Double is called by the Ergo package through ABIInternal.
func Double(x int) int { return 2 * x }

// Inits is how many times this package's init ran.
func Inits() int { return inits }
