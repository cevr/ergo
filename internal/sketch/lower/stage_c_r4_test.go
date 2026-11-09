package lower

import "testing"

// Found by TestCheckCorpusBuilds: a union of two instances of one generic
// record, V[A] | V[B], declares its marker method once, on V, and a match
// over it tells the instances apart.
func TestErgoRunUnionOfOneGenericRecord(t *testing.T) {
	needsBuild(t)
	wantRun(t, `type A {}
type B {}
type V[T] {}
type W[T, Q] {}
type P = V[A] | V[B]
type G[T] = W[T, A] | W[T, []T]

func h(p P) int {
	match p {
	case V[A]: 1
	case V[B]: 2
	}
}
func k(g G[B]) int {
	match g {
	case W[B, A]: 3
	case W[B, []B]: 4
	}
}

func main() needs Console {
	if h(V[A]) == 1 && h(V[B]) == 2 && k(W[B, A]) == 3 && k(W[B, []B]) == 4 {
		Console.Println("instances")
	}
}
`, "instances\n")
}
