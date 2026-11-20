// Command control is namedrowbench's hand Go twin
// (benchmarks/namedrow/cmd/namedrowbench): the same calls failing one of
// six error types, as a person writing Go handles them: an error result,
// then an errors.As chain that maps the failure to its code, run as
// `control bench KIND N`. KIND named and spelled run the same code: Go has
// one way to write it.
package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
)

type Unique struct{}

func (Unique) Error() string { return "Unique" }

type Serialization struct{}

func (Serialization) Error() string { return "Serialization" }

type Deadlock struct{}

func (Deadlock) Error() string { return "Deadlock" }

type Timeout struct{}

func (Timeout) Error() string { return "Timeout" }

type Closed struct{}

func (Closed) Error() string { return "Closed" }

type Other struct{}

func (Other) Error() string { return "Other" }

//go:noinline
func call(i int) (int, error) {
	switch i % 7 {
	case 0:
		return 0, Unique{}
	case 1:
		return 0, Serialization{}
	case 2:
		return 0, Deadlock{}
	case 3:
		return 0, Timeout{}
	case 4:
		return 0, Closed{}
	case 5:
		return 0, Other{}
	}
	return i, nil
}

func code(err error) int {
	var u Unique
	var s Serialization
	var d Deadlock
	var t Timeout
	var c Closed
	var o Other
	switch {
	case errors.As(err, &u):
		return 1
	case errors.As(err, &s):
		return 2
	case errors.As(err, &d):
		return 3
	case errors.As(err, &t):
		return 4
	case errors.As(err, &c):
		return 5
	case errors.As(err, &o):
		return 6
	}
	panic(err)
}

func loop(n int) int {
	h := 0
	for i := 0; i < n; i++ {
		v, err := call(i)
		if err != nil {
			v = code(err)
		}
		h += v
	}
	return h
}

func main() {
	if len(os.Args) != 4 || os.Args[1] != "bench" || os.Args[2] != "named" && os.Args[2] != "spelled" {
		fmt.Fprintln(os.Stderr, "usage: control bench named|spelled N")
		os.Exit(2)
	}
	n, err := strconv.Atoi(os.Args[3])
	if err != nil {
		fmt.Fprintln(os.Stderr, "control: N is not a count")
		os.Exit(2)
	}
	fmt.Println(loop(n))
}
