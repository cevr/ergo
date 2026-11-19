package v1

// GoReadsCheck is the test build's guard of a `reads` fact (LAWFFI LF1;
// docs/spec/interop.md, Foreign facts). Where the run build passes a live
// slice to a Go function whose binding says it only reads the operand,
// the test build passes given, a copy of held, and calls this after the
// call: a Go write to given is a GoReadsDefect naming the function, the
// parameter and the Ergo call. Elements compare with ==, two NaNs as
// equal; a write of -0 over +0 is not seen.
func GoReadsCheck[S ~[]E, E comparable](held, given S, fn, param, at string) {
	if len(held) != len(given) {
		panic(&GoReadsDefect{Func: fn, Param: param, At: at})
	}
	for i, a := range held {
		b := given[i]
		if a != b && (a == a || b == b) {
			panic(&GoReadsDefect{Func: fn, Param: param, At: at})
		}
	}
}

// GoReadsDefect is the defect of a Go call that wrote an operand its
// binding says it only reads: the `// ergo:reads` line is false for this
// call, and the run build, which passes the operand with no copy, would
// change a value Ergo holds.
type GoReadsDefect struct {
	Func, Param, At string
}

// String is the defect's text, which the entry's report writes after
// "panic: ".
func (d *GoReadsDefect) String() string {
	return "Go wrote " + d.Param + ", which its binding says " + d.Func + " only reads, at " + d.At
}
