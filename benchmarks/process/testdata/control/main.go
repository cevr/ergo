// Command control is the hand Go twin of benchmarks/process: each probe
// as a person writes it in Go with the guarantees process.Run keeps (a
// child goroutine whose panic is a value, a wait that a stop of the
// waiter ends, and for timed a deadline that stops the child), printing
// what the Ergo probe prints (PROCESS-RUN; opcount builds it as the
// control). It is errgroup's shape with recover and context.WithTimeout.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

// crashed is the panic of a child, as a value.
type crashed struct{ reason any }

func (c crashed) Error() string { return fmt.Sprint(c.reason) }

// run runs block on its own goroutine under ctx and gives its value, or
// the child's panic as an error, or ctx's end.
func run(ctx context.Context, block func(context.Context) int) (int, error) {
	type result struct {
		v   int
		err error
	}
	res := make(chan result, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				res <- result{err: crashed{r}}
			}
		}()
		res <- result{v: block(ctx)}
	}()
	select {
	case r := <-res:
		return r.v, r.err
	case <-ctx.Done():
		<-res // the child sees ctx and ends: drain it
		return 0, ctx.Err()
	}
}

func probe(ctx context.Context, n int, limit time.Duration) (int, error) {
	acc := 0
	for i := 0; i < n; i++ {
		c, cancel := ctx, context.CancelFunc(func() {})
		if limit > 0 {
			c, cancel = context.WithTimeout(ctx, limit)
		}
		v, err := run(c, func(context.Context) int { return i & 3 })
		cancel()
		if err != nil {
			return 0, err
		}
		acc += v
	}
	return acc, nil
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
	var limit time.Duration
	switch os.Args[2] {
	case "run":
	case "timed":
		limit = time.Hour
	default:
		os.Exit(2)
	}
	ctx, stop := context.WithCancel(context.Background())
	acc, err := probe(ctx, n, limit)
	stop()
	if err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(acc)
}
