package lower

import (
	"testing"
)

// TestStdTextRuns: each function of std strings and strconv (stdlib.md,
// strings and strconv) calls its runtime/v1 body, or its Ergo body for
// strings.Cut, and prints one line. strconv.Atoi fails strconv.Malformed:
// a catch arm takes it, and main's uncaught Malformed is written with its
// text and status 1.
func TestStdTextRuns(t *testing.T) {
	needsBuild(t)
	runBehaviors(t, []behavior{
		{name: "text", src: `import strings
import strconv

func cut(s string, sep string) string {
	match strings.Cut(s, sep) {
	case Some[strings.Halves] as h:
		"<\(h.Value.Before)|\(h.Value.After)>"
	case None:
		"none"
	}
}

func atoi(s string) string {
	let n = strconv.Atoi(s) catch {
	case strconv.Malformed as m:
		return "malformed \(m.Text)"
	case strconv.OutOfRange as o:
		return "out of range \(o.Text)"
	}
	"int \(n)"
}

func main() needs Console {
	Console.Println("[\(strings.Join(strings.Split("a,b,,c", ","), "|"))]")
	Console.Println("\(len(strings.Split("a,b,,c", ",")))")
	Console.Println(strings.Join([]string{"x", "y", "z"}, "-"))
	Console.Println("\(strings.HasPrefix("prefix", "pre")) \(strings.HasPrefix("prefix", "fix"))")
	Console.Println("\(strings.HasSuffix("suffix", "fix")) \(strings.HasSuffix("suffix", "suf"))")
	Console.Println("[\(strings.TrimSpace("  \t pad \n"))]")
	Console.Println(strings.TrimPrefix("v1.2", "v"))
	Console.Println(strings.TrimSuffix("doc.md", ".md"))
	Console.Println("[\(strings.Join(strings.Fields("  one two\tthree  "), "|"))]")
	Console.Println(strings.Repeat("ab", 3))
	Console.Println(strings.ToLower("MiXeD"))
	Console.Println(strings.ToUpper("MiXeD"))
	Console.Println("\(strings.Index("chicken", "ken")) \(strings.Index("chicken", "dmr"))")
	Console.Println("\(strings.Contains("seafood", "foo")) \(strings.Contains("seafood", "bar"))")
	Console.Println(strings.Slice("hello world", 6, 11))
	Console.Println(cut("key: value", ": "))
	Console.Println(cut("a: b", "a"))
	Console.Println(cut("nosep", ":"))
	Console.Println(strconv.Itoa(-42))
	Console.Println(atoi("123"))
	Console.Println(atoi("x"))
	Console.Println(strconv.Quote("say \"hi\"\n"))
}
`, stdout: "[a|b||c]\n4\nx-y-z\ntrue false\ntrue false\n[pad]\n1.2\ndoc\n[one|two|three]\nababab\nmixed\nMIXED\n4 -1\ntrue false\nworld\n<key|value>\n<|: b>\nnone\n-42\nint 123\nmalformed x\n\"say \\\"hi\\\"\\n\"\n"},
		{name: "atoiUncaught", src: `import strconv

func main() fails strconv.Malformed | strconv.OutOfRange needs Console {
	let n = try strconv.Atoi("12x")
	Console.Println("\(n)")
}
`, exit: 1, stderr: []string{"error: strconv.Malformed{Text:12x}\n"}},
	})
}
