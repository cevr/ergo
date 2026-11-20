package lower

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// aloneSrc hands an Ergo function to Go three ways: to strings.IndexFunc,
// a non-escaping callback that Go calls only during the call on its
// caller's goroutine (v1.GoSyncCallbacks); to
// crypto/subtle.WithDataIndependentTiming, whose binding is escaping (no
// reviewed row says otherwise), as time.AfterFunc's is: Go may call such a
// callback later, from a goroutine of its own; and as a sort.Interface
// value, whose methods Go may call from anywhere once it holds it. Its
// owned Ref sites differ in their literal's footprint: bump's and fill's
// (the in-place form) are empty, spawn's starts a task.
const aloneSrc = `import go strings
import go crypto/subtle
import go sort
import sync
import task

type N int implements sort.Interface

func Len(r N) int { 0 }
func Less(r N, i int, j int) bool { i < j }
func Swap(r N, i int, j int) {}

func scan(s string) int {
	strings.IndexFunc(s, func(r rune) bool { r == 'x' })
}

func later() needs go.Foreign {
	subtle.WithDataIndependentTiming(func() {})
}

func conv(x N) sort.Interface { x }

func bump(r sync.Ref[[]int]) {
	r.Update(func(v []int) []int { v.Append(1) })
}

func fill(r sync.Ref[Map[int, int]]) {
	r.Update(func(m Map[int, int]) Map[int, int] {
		let mut c = m
		c[1] = 1
		c
	})
}

func spawn(r sync.Ref[[]int]) {
	r.Update(func(v []int) []int {
		let h = task.Go(func() int { 1 })
		v.Append(h.Wait())
	})
}

func main() needs go.Foreign {
	if scan("axb") == 1 {
		later()
	}
	let _ = conv(N(1)).Len()
	let r = sync.NewRef[[]int]([]int{})
	bump(r)
	fill(sync.NewRef[Map[int, int]](Map[int, int]{}))
	spawn(r)
}
`

// TestHandedCallbackEndsAlone: the program ends rt.Alone (v1.GoHanded)
// before it hands Go an escaping callback, on both lowerings (the text
// emitter's goCallbacks and the IR's hostArgs), and before it converts a
// value to a Go interface (goInterface, which only the text emitter
// lowers), and not before a non-escaping callback. A Ref skips its lock
// while the program is alone, so an escaping callback or an interface
// value with no GoHanded before it could run on Go's goroutine against an
// unlocked Ref (the mutants "gohanded-off", "ir-gohanded-off" and
// "goiface-gohanded-off"); a GoHanded before IndexFunc would only give up
// the unlocked Ref in a program that runs one task ("gohanded-sync").
// The form is the promise here: GoHanded is the one fact checked.
func TestHandedCallbackEndsAlone(t *testing.T) {
	text, ir, onIR := lowerAlone(t)
	for _, c := range []struct {
		fn   string
		want int
	}{{"scan", 0}, {"later", 1}, {"conv", 1}} {
		if got := handedIn(text, "func "+c.fn+"("); got != c.want {
			t.Errorf("text: %s has %d GoHanded, want %d:\n%s", c.fn, got, c.want, text)
		}
	}
	if !slices.Contains(onIR, "later") || !slices.Contains(onIR, "scan") {
		t.Fatalf("want scan and later on the IR, got %v", onIR)
	}
	want := 1
	if slices.Contains(onIR, "conv") {
		want++
	}
	if got := strings.Count(ir, `"GoHanded"`); got != want {
		t.Errorf("IR: want %d GoHanded (later's, and conv's if conv is on the IR), got %d", want, got)
	}
}

// TestOwnedSiteAloneFollowsFootprint: an owned Ref site tells the cell it
// may skip its lock while the program is alone (the alone flag, refown.go)
// exactly when its literal's footprint is empty, on both lowerings. bump's
// literal appends; spawn's starts a task, which could use the cell before
// the Update stores (runtime/v1 TestRefFlipInsideUpdate), so its site
// locks (the mutant "site-alone-always"; "site-alone-dropped" for a
// lowering that never passes alone).
func TestOwnedSiteAloneFollowsFootprint(t *testing.T) {
	text, ir, onIR := lowerAlone(t)
	last := regexp.MustCompile(`, (true|false), (true|false)\)`)
	for _, c := range []struct{ fn, want string }{{"bump", "true"}, {"fill", "true"}, {"spawn", "false"}} {
		body := funcText(text, "func "+c.fn+"(")
		m := last.FindAllStringSubmatch(body, -1)
		if !strings.Contains(body, "RefUpdate") || len(m) == 0 {
			t.Errorf("text: %s has no owned Update:\n%s", c.fn, body)
			continue
		}
		if got := m[len(m)-1][2]; got != c.want {
			t.Errorf("text: %s's owned Update is told alone %s, want %s:\n%s", c.fn, got, c.want, body)
		}
	}
	var alone []string
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			if name, _ := v["name"].(string); strings.HasPrefix(name, "RefUpdate") {
				if args, _ := v["args"].([]any); len(args) > 0 {
					a, _ := args[len(args)-1].(map[string]any)
					s, _ := a["v"].(string)
					alone = append(alone, s)
				}
			}
			for _, x := range v {
				walk(x)
			}
		case []any:
			for _, x := range v {
				walk(x)
			}
		}
	}
	var doc any
	if err := json.Unmarshal([]byte(ir), &doc); err != nil {
		t.Fatal(err)
	}
	walk(doc)
	// spawn's frame owns a task, which the IR leaves to the emitter.
	if !slices.Contains(onIR, "bump") || !slices.Contains(onIR, "fill") {
		t.Fatalf("want bump and fill on the IR, got %v", onIR)
	}
	want := []string{"true", "true"}
	if slices.Contains(onIR, "spawn") {
		want = append(want, "false")
	}
	slices.Sort(alone)
	slices.Sort(want)
	if !slices.Equal(alone, want) {
		t.Errorf("IR: the owned Updates are told alone %v, want %v (bump and fill true, spawn false)", alone, want)
	}
}

// lowerAlone lowers aloneSrc as text and on the IR: the text, the IR
// file and the functions the IR lowered.
func lowerAlone(t *testing.T) (text, ir string, onIR []string) {
	t.Helper()
	o := lowerSource("alone", aloneSrc, true)
	if o.err != nil || len(o.diags) > 0 || o.refused != nil || o.panicked != nil {
		t.Fatalf("lowering failed: %v %v %v %v", o.err, o.diags, o.refused, o.panicked)
	}
	u := Unit{Package: o.pkg, Files: []*syntax.File{o.file}, Sources: []Source{{Path: testSourcePath, Text: aloneSrc}}}
	files, rep, err := LowerIR(u)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.Path == IRFileName {
			ir = string(f.Data)
		}
	}
	return goText(o), ir, rep.IR
}

// funcText is the Go function of text that starts at head, up to the
// next top-level func.
func funcText(text, head string) string {
	i := strings.Index(text, head)
	if i < 0 {
		return ""
	}
	body := text[i+len(head):]
	if j := strings.Index(body, "\nfunc "); j >= 0 {
		body = body[:j]
	}
	return body
}

// handedIn counts the GoHanded calls in the Go function of text that
// starts at head, or -1 when there is none.
func handedIn(text, head string) int {
	if !strings.Contains(text, head) {
		return -1
	}
	return strings.Count(funcText(text, head), ".GoHanded()")
}

// streamAloneSrc decodes a json Stream over a cut text.
const streamAloneSrc = `import io
import json

func main() needs Console {
	let c = json.Codec[json.Value]{MaxBytes: 0, MaxDepth: 0}
	let v = json.DecodeStream(c, io.CutAt("[1, \"a\", {\"k\": true}]", []int{3, 9})) catch {
	case _:
		Console.Println("failed")
		return
	}
	Console.Println(json.Encode(c, v))
}
`

// TestStreamOperationsRunAlone: json's Stream reads its source only in
// refill and in Settle's count past a break, outside the token
// operations (stream.ergo, fill), so on the IR every other operation of
// its state is told alone and runs unlocked while the program is.
func TestStreamOperationsRunAlone(t *testing.T) {
	o := lowerSource("streamalone", streamAloneSrc, true)
	if o.err != nil || len(o.diags) > 0 || o.refused != nil || o.panicked != nil {
		t.Fatalf("lowering failed: %v %v %v %v", o.err, o.diags, o.refused, o.panicked)
	}
	u := Unit{Package: o.pkg, Files: []*syntax.File{o.file}, Sources: []Source{{Path: testSourcePath, Text: streamAloneSrc}}}
	files, _, err := LowerIR(u)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Funcs []struct {
			Name string `json:"name"`
			Body any    `json:"body"`
		} `json:"funcs"`
	}
	for _, f := range files {
		if f.Path == IRFileName {
			if err := json.Unmarshal(f.Data, &doc); err != nil {
				t.Fatal(err)
			}
		}
	}
	sites := map[string][]string{}
	var walk func(fn string, v any)
	walk = func(fn string, v any) {
		switch v := v.(type) {
		case map[string]any:
			if name, _ := v["name"].(string); strings.HasPrefix(name, "RefModify") || strings.HasPrefix(name, "RefUpdate") {
				if args, _ := v["args"].([]any); len(args) > 0 {
					a, _ := args[len(args)-1].(map[string]any)
					s, _ := a["v"].(string)
					sites[fn] = append(sites[fn], s)
				}
			}
			for _, x := range v {
				walk(fn, x)
			}
		case []any:
			for _, x := range v {
				walk(fn, x)
			}
		}
	}
	for _, f := range doc.Funcs {
		if strings.HasPrefix(f.Name, "Stream") {
			walk(f.Name, f.Body)
		}
	}
	ops := 0
	for fn, flags := range sites {
		op := fn[strings.LastIndex(fn, ".")+1:]
		for _, a := range flags {
			switch {
			case op == "refill":
				if a != "false" {
					t.Errorf("refill's read is told alone %s, want false", a)
				}
			case op == "Settle":
			case a != "true":
				t.Errorf("%s's operation is told alone %s, want true", fn, a)
			default:
				ops++
			}
		}
	}
	if ops < 5 || !hasSuffixKey(sites, ".refill") {
		t.Errorf("want the Stream's operations and refill on the IR, got %v", sites)
	}
}

func hasSuffixKey(m map[string][]string, suffix string) bool {
	for k := range m {
		if strings.HasSuffix(k, suffix) {
			return true
		}
	}
	return false
}
