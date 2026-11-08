package unionreprbench

import "strconv"

// Question 4: a large slice of union values under the GC, in forms A, D
// and E, with no pointer in the payload (NP) and with a string in it (WP).
//
//	type NP = NP1{A int} | NP2{A int, B int}
//	type WP = WP1{S string} | WP2{S string, A int}

type NP1 struct{ A int }
type NP2 struct{ A, B int }
type WP1 struct{ S string }
type WP2 struct {
	S string
	A int
}

type NPA interface{ isNPA() }
type WPA interface{ isWPA() }

func (NP1) isNPA() {}
func (NP2) isNPA() {}
func (WP1) isWPA() {}
func (WP2) isWPA() {}

// NPD: NP1.A and NP2.A share one int slot.
type NPD struct {
	tag  uint8
	a, b int
}

// WPD: WP1.S and WP2.S share one string slot.
type WPD struct {
	tag uint8
	s   string
	a   int
}

type NPE struct {
	tag uint8
	p   any
}

type WPE struct {
	tag uint8
	p   any
}

// gcStrings are the strings of WP values: built once, so a WP value adds
// no string allocation of its own; only its box (form A, E) is new.
var gcStrings = func() []string {
	out := make([]string, 64)
	for i := range out {
		out[i] = "endpoint-" + strconv.Itoa(i)
	}
	return out
}()

func buildNPA(raw []rawValue) []NPA {
	out := make([]NPA, len(raw))
	for i, r := range raw {
		if r.k == 0 {
			out[i] = NP1{A: r.x}
		} else {
			out[i] = NP2{A: r.x, B: r.x + 1}
		}
	}
	return out
}

func buildNPD(raw []rawValue) []NPD {
	out := make([]NPD, len(raw))
	for i, r := range raw {
		if r.k == 0 {
			out[i] = NPD{tag: 1, a: r.x}
		} else {
			out[i] = NPD{tag: 2, a: r.x, b: r.x + 1}
		}
	}
	return out
}

func buildNPE(raw []rawValue) []NPE {
	out := make([]NPE, len(raw))
	for i, r := range raw {
		if r.k == 0 {
			out[i] = NPE{1, NP1{A: r.x}}
		} else {
			out[i] = NPE{2, NP2{A: r.x, B: r.x + 1}}
		}
	}
	return out
}

func buildWPA(raw []rawValue) []WPA {
	out := make([]WPA, len(raw))
	for i, r := range raw {
		s := gcStrings[r.x%len(gcStrings)]
		if r.k == 0 {
			out[i] = WP1{S: s}
		} else {
			out[i] = WP2{S: s, A: r.x}
		}
	}
	return out
}

func buildWPD(raw []rawValue) []WPD {
	out := make([]WPD, len(raw))
	for i, r := range raw {
		s := gcStrings[r.x%len(gcStrings)]
		if r.k == 0 {
			out[i] = WPD{tag: 1, s: s}
		} else {
			out[i] = WPD{tag: 2, s: s, a: r.x}
		}
	}
	return out
}

func buildWPE(raw []rawValue) []WPE {
	out := make([]WPE, len(raw))
	for i, r := range raw {
		s := gcStrings[r.x%len(gcStrings)]
		if r.k == 0 {
			out[i] = WPE{1, WP1{S: s}}
		} else {
			out[i] = WPE{2, WP2{S: s, A: r.x}}
		}
	}
	return out
}

// The churn step: a value is replaced by the next one (NP1 -> NP2 -> NP1),
// as a machine replaces its state.

//go:noinline
func nextNPA(u NPA) NPA {
	switch v := u.(type) {
	case NP1:
		return NP2{A: v.A + 1, B: v.A}
	case NP2:
		return NP1{A: v.A + v.B}
	}
	panic("ergo: defect: no case of the match matched")
}

//go:noinline
func nextNPD(u NPD) NPD {
	switch u.tag {
	case 1:
		return NPD{tag: 2, a: u.a + 1, b: u.a}
	case 2:
		return NPD{tag: 1, a: u.a + u.b}
	}
	panic("ergo: defect: no case of the match matched")
}

//go:noinline
func nextNPE(u NPE) NPE {
	switch u.tag {
	case 1:
		v := u.p.(NP1)
		return NPE{2, NP2{A: v.A + 1, B: v.A}}
	case 2:
		v := u.p.(NP2)
		return NPE{1, NP1{A: v.A + v.B}}
	}
	panic("ergo: defect: no case of the match matched")
}

//go:noinline
func nextWPA(u WPA) WPA {
	switch v := u.(type) {
	case WP1:
		return WP2{S: v.S, A: len(v.S) + 256}
	case WP2:
		return WP1{S: v.S}
	}
	panic("ergo: defect: no case of the match matched")
}

//go:noinline
func nextWPD(u WPD) WPD {
	switch u.tag {
	case 1:
		return WPD{tag: 2, s: u.s, a: len(u.s) + 256}
	case 2:
		return WPD{tag: 1, s: u.s}
	}
	panic("ergo: defect: no case of the match matched")
}

//go:noinline
func nextWPE(u WPE) WPE {
	switch u.tag {
	case 1:
		v := u.p.(WP1)
		return WPE{2, WP2{S: v.S, A: len(v.S) + 256}}
	case 2:
		v := u.p.(WP2)
		return WPE{1, WP1{S: v.S}}
	}
	panic("ergo: defect: no case of the match matched")
}

func (u NPD) wellFormed() bool {
	switch u.tag {
	case 1:
		return u.b == 0
	case 2:
		return true
	}
	return false
}

func (u WPD) wellFormed() bool {
	switch u.tag {
	case 1:
		return u.a == 0
	case 2:
		return true
	}
	return false
}

// Canonical text, for the contract test.

func textNPA(u NPA) string {
	switch v := u.(type) {
	case NP1:
		return "NP1{" + strconv.Itoa(v.A) + "}"
	case NP2:
		return "NP2{" + strconv.Itoa(v.A) + "," + strconv.Itoa(v.B) + "}"
	}
	panic("no variant")
}

func textNPD(u NPD) string {
	switch u.tag {
	case 1:
		return "NP1{" + strconv.Itoa(u.a) + "}"
	case 2:
		return "NP2{" + strconv.Itoa(u.a) + "," + strconv.Itoa(u.b) + "}"
	}
	panic("no variant")
}

func textNPE(u NPE) string {
	switch u.tag {
	case 1:
		return textNPA(u.p.(NP1))
	case 2:
		return textNPA(u.p.(NP2))
	}
	panic("no variant")
}

func textWPA(u WPA) string {
	switch v := u.(type) {
	case WP1:
		return "WP1{" + v.S + "}"
	case WP2:
		return "WP2{" + v.S + "," + strconv.Itoa(v.A) + "}"
	}
	panic("no variant")
}

func textWPD(u WPD) string {
	switch u.tag {
	case 1:
		return "WP1{" + u.s + "}"
	case 2:
		return "WP2{" + u.s + "," + strconv.Itoa(u.a) + "}"
	}
	panic("no variant")
}

func textWPE(u WPE) string {
	switch u.tag {
	case 1:
		return textWPA(u.p.(WP1))
	case 2:
		return textWPA(u.p.(WP2))
	}
	panic("no variant")
}
