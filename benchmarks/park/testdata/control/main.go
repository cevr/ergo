// Command control is the hand Go twin of benchmarks/park: each probe as a
// person would write it in Go for speed, with the guarantees the Ergo
// probe keeps (a wait that a stop of the waiter ends), printing what the
// Ergo probe prints (PARK1; opcount builds it as the control).
package main

import (
	"fmt"
	"os"
	"strconv"
)

// spawn is a spawn and a join: a goroutine, its result on a channel of
// one, and a receive that a stop of the waiter also ends.
func spawn(n int, stop <-chan struct{}) int {
	acc := 0
	for i := 0; i < n; i++ {
		res := make(chan int, 1)
		go func() { res <- i & 3 }()
		select {
		case v := <-res:
			acc += v
		case <-stop:
			return acc
		}
	}
	return acc
}

type request struct {
	e     int
	reply chan int
}

// ask is a request and its reply between two goroutines: a counter that
// owns its state, an inbox with no buffer, and a reply channel of one that
// the asker reuses; each wait also ends on a stop.
func ask(n int, stop <-chan struct{}) int {
	inbox := make(chan request)
	go func() {
		s := 0
		for {
			select {
			case r := <-inbox:
				s += r.e
				r.reply <- s
			case <-stop:
				return
			}
		}
	}()
	reply := make(chan int, 1)
	acc := 0
	for i := 0; i < n; i++ {
		select {
		case inbox <- request{e: i & 3, reply: reply}:
		case <-stop:
			return acc
		}
		select {
		case v := <-reply:
			acc = (acc + v) & 0xffff
		case <-stop:
			return acc
		}
	}
	return acc
}

func main() {
	if len(os.Args) != 4 || os.Args[1] != "bench" {
		fmt.Fprintln(os.Stderr, "usage: control bench KIND N")
		os.Exit(2)
	}
	n, err := strconv.Atoi(os.Args[3])
	if err != nil {
		os.Exit(2)
	}
	stop := make(chan struct{})
	switch os.Args[2] {
	case "spawn":
		fmt.Println(spawn(n, stop))
	case "ask":
		fmt.Println(ask(n, stop))
	default:
		os.Exit(2)
	}
	close(stop)
}
