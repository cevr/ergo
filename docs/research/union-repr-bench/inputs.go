package unionreprbench

// rawValue is one value of a generated union before a form holds it: the
// variant index k and the int field x.
type rawValue struct{ k, x int }

// rawSingles is n seeded values of a union of v variants. Each x is at
// least 256, so form A boxes it on the heap: Go keeps a static copy of the
// small integers 0 to 255 (runtime.staticuint64s) and boxes those without
// an allocation.
func rawSingles(n, v int, seed uint64) []rawValue {
	r := newRand(seed)
	out := make([]rawValue, n)
	for i := range out {
		out[i] = rawValue{k: int(r.next() % uint64(v)), x: 256 + int(r.next()%1000)}
	}
	return out
}

func rawPairs(n, p, q int, seed uint64) [][2]rawValue {
	r := newRand(seed)
	out := make([][2]rawValue, n)
	for i := range out {
		out[i] = [2]rawValue{
			{k: int(r.next() % uint64(p)), x: 256 + int(r.next()%1000)},
			{k: int(r.next() % uint64(q)), x: 256 + int(r.next()%1000)},
		}
	}
	return out
}
