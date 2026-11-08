package check_test

import "testing"

// TestSelfCallOwnTypeParameters (round 6b1 ruling; round 6 review m1): a
// first parameter written as the function's own type parameter is that
// parameter, even beside a package type of the same name, so the function
// is an ordinary generic function and attaches to no type. A function with
// type parameters beyond those its first parameter binds can be dot-called,
// but it cannot satisfy an operation, which takes no type parameters: S106
// at the satisfaction site, naming the extra parameter.
func TestSelfCallOwnTypeParameters(t *testing.T) {
	checkEdgeCases(t, []edgeCase{
		{"a bracket name shadows a package type", `
type T { n int }
type Show { show() string }
func show[T](x T) string { return "generic" }
let W = context { Show: T{n: 1} }
func main() {
	let t = T{n: 1}
	_ = show(t)
}
`, []edgeWant{{"S106", "T has no method show", 5}}},
		{"an extra type parameter the signature does not use", `
type Box[T] { v T }
type Show { show() string }
func show[T, U](b Box[T]) string { return "box" }
let W = context { Show: Box[int]{v: 1} }
func main() {}
`, []edgeWant{{"S106", "has a type parameter of its own, U", 5}}},
		{"an extra type parameter in a parameter", `
type Getter { Pair(u int) int }
type Box[T] { v T }
func Pair[T, U](b Box[T], u U) U { return u }
let W = context { Getter: Box[int]{v: 1} }
func main() {}
`, []edgeWant{{"S106", "has a type parameter of its own, U", 5}}},
		{"a non-generic owner and a type parameter", `
type Fixed { n int }
type Show { show() string }
func show[U](f Fixed) string { return "fixed" }
let W = context { Show: Fixed{n: 1} }
func main() {}
`, []edgeWant{{"S106", "has a type parameter of its own, U", 5}}},
		{"the owner's parameters only", `
type Box[T] { v T }
type Show { show() string }
func show[T](b Box[T]) string { return "box" }
let W = context { Show: Box[int]{v: 1} }
func main() {}
`, nil},
	})
}
