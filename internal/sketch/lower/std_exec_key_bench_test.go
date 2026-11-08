package lower

import "testing"

// BenchmarkExecKey (std-strengths SS1): exec.Key of a command with 10
// arguments, the Ergo body emitted and measured in process, against hand
// Go strings.Join of the name and the arguments with a NUL byte. Run at
// the lane base for the old body (a concatenation per argument) and at
// the head for the new one (slices.WithCapacity, then strings.Join).
func BenchmarkExecKey(b *testing.B) {
	const src = `import exec

let command = exec.New("git", []string{"log", "--oneline", "-n", "10", "--format=%h %s", "--no-merges", "--first-parent", "main", "--", "src"})

func KeyLen() int { len(exec.Key(command)) }

func main() needs Console {
	Console.Println("\(KeyLen())")
}
`
	emittedBench(b, "execkey", src, func(mainGo string) string {
		return `package main

import (
	"strings"
	"testing"
)

var keyName = "git"
var keyArgs = []string{"log", "--oneline", "-n", "10", "--format=%h %s", "--no-merges", "--first-parent", "main", "--", "src"}
var sink int

func BenchmarkKeyErgo(b *testing.B) {
	// The package's lets are set by main, which a benchmark does not run;
	// command is the emitted exec.Command with the same name and arguments.
	command = Command{Name: keyName, Args: keyArgs}
	for range b.N {
		sink = ` + emittedCall(mainGo, "KeyLen", "") + `
	}
}

func BenchmarkKeyGo(b *testing.B) {
	for range b.N {
		sink = len(strings.Join(append([]string{keyName}, keyArgs...), "\x00"))
	}
}
`
	}, "KeyErgo", "KeyGo")
}
