package lower

import "testing"

// BenchmarkGoContextCall is IG10's control: a Go call whose hidden first
// parameter is the task's context.Context (runtime/trace.Log, which reads
// the context and returns at once when tracing is off), lowered with
// rt.GoContext and the interruption point after it, against hand Go that
// passes a context it holds, in the same binary. Target: equal within
// noise (the task's context is one pointer in an interface, no
// allocation; the stop channel is made only when Go asks for Done).
func BenchmarkGoContextCall(b *testing.B) {
	const src = `import go runtime/trace

func Logs(n int) int needs go.Foreign {
	trace.Log("c", "m")
	n
}

func main() needs Console | go.Foreign {
	Console.Println("\(Logs(1))")
}
`
	gen := func(mainGo string) string {
		return `package main

import (
	"context"
	"runtime/trace"
	"testing"
)

var sink int

var held = context.Background()

func logsGo(ctx context.Context, n int) int {
	trace.Log(ctx, "c", "m")
	return n
}

func BenchmarkLogErgo(b *testing.B) {
	b.ReportAllocs()
	for i := range b.N {
		sink = ` + emittedCall(mainGo, "Logs", "i") + `
	}
}

func BenchmarkLogGo(b *testing.B) {
	b.ReportAllocs()
	for i := range b.N {
		sink = logsGo(held, i)
	}
}
`
	}
	emittedBench(b, "gocontext_call", src, gen, "LogErgo", "LogGo")
}
