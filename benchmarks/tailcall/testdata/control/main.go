// Command control is tailbench's hand Go twin
// (benchmarks/tailcall/cmd/tailbench), run as `control bench KIND N`:
//   - chain: the same chain of functions, each returning (int, error) and
//     the next one's call, and a type switch on the error where the loop
//     catches it;
//   - trylet: a function that passes c0's error on (`if err != nil`);
//   - orfail: a Map read, comma-ok, with the error built on the miss only;
//   - slotget: a tagged box read by a switch, its failure an error.
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

func tryLet(x int) (int, error) {
	v, err := c0(x)
	if err != nil {
		return 0, err
	}
	return v + 1, nil
}

func look(m map[int]int, k int) (int, error) {
	v, ok := m[k]
	if !ok {
		return 0, gone{At: k}
	}
	return v, nil
}

// box is a Box[int, gone] by hand: a tag and both payloads.
type box struct {
	tag   uint8 // 0 idle, 1 got, 2 bad
	value int
	err   gone
}

func boxOf(i int) box {
	if i%3 == 0 {
		return box{tag: 2, err: gone{At: i}}
	}
	if i%7 == 1 {
		return box{}
	}
	return box{tag: 1, value: i}
}

func get(b box) (int, error) {
	switch b.tag {
	case 0:
		return 0, bad{Why: 0}
	case 1:
		return b.value, nil
	}
	return 0, b.err
}

// caught is the loop's catch: the arm's value of err, or v.
func caught(v int, err error) int {
	switch e := err.(type) {
	case nil:
		return v
	case gone:
		return e.At & 1
	case bad:
		return e.Why & 3
	}
	return 0
}

func main() {
	if len(os.Args) != 4 || os.Args[1] != "bench" {
		fmt.Fprintln(os.Stderr, "usage: control bench KIND N")
		os.Exit(2)
	}
	n, err := strconv.Atoi(os.Args[3])
	if err != nil {
		fmt.Fprintln(os.Stderr, "control: N is not a count")
		os.Exit(2)
	}
	t := 0
	switch os.Args[2] {
	case "chain":
		for i := 0; i < n; i++ {
			t += caught(c3(i & 4095))
		}
	case "trylet":
		for i := 0; i < n; i++ {
			t += caught(tryLet(i & 4095))
		}
	case "slotget", "slotfield", "slotlit":
		for i := 0; i < n; i++ {
			t += caught(get(boxOf(i & 4095)))
		}
	case "orfail":
		m := map[int]int{}
		for i := 0; i < 1024; i++ {
			m[i*2] = i
		}
		for i := 0; i < n; i++ {
			t += caught(look(m, i&2047))
		}
	default:
		fmt.Fprintln(os.Stderr, "control: no such kind")
		os.Exit(2)
	}
	fmt.Println(t)
}
