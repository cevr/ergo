// Command control is tailbench's hand Go twin
// (benchmarks/tailcall/cmd/tailbench), run as `control bench chain N`: the
// same chain of functions, each returning (int, error) and the next one's
// call, and a type switch on the error where the loop catches it.
package main

import (
	"fmt"
	"os"
	"strconv"
)

type gone struct{ At int }

func (gone) Error() string { return "gone" }

type bad struct{ Why int }

func (bad) Error() string { return "bad" }

func c0(x int) (int, error) {
	if x%7 == 0 {
		return 0, gone{At: x}
	}
	if x%11 == 1 {
		return 0, bad{Why: x}
	}
	return x * 3, nil
}

func c1(x int) (int, error) {
	if x > 100000 {
		return x, nil
	}
	return c0(x + 1)
}

func c2(x int) (int, error) {
	if x%5 == 2 {
		return c1(x + 2)
	}
	return c1(x - 1)
}

func c3(x int) (int, error) {
	return c2(x * 2)
}

func main() {
	if len(os.Args) != 4 || os.Args[1] != "bench" || os.Args[2] != "chain" {
		fmt.Fprintln(os.Stderr, "usage: control bench chain N")
		os.Exit(2)
	}
	n, err := strconv.Atoi(os.Args[3])
	if err != nil {
		fmt.Fprintln(os.Stderr, "control: N is not a count")
		os.Exit(2)
	}
	t := 0
	for i := 0; i < n; i++ {
		v, err := c3(i & 4095)
		if err != nil {
			switch e := err.(type) {
			case gone:
				v = e.At & 1
			case bad:
				v = e.Why & 3
			}
		}
		t += v
	}
	fmt.Println(t)
}
