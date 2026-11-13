package lower

import "testing"

// providerErgoSrc is a program whose provider's field type is declared in
// Ergo (patterns.md, Provider plans 3): Rec, its NewRec and its Call. Its
// capability has operations of no, one and two arguments, one that fails,
// and one with no result; main answers them all, calls each once through
// the provider, and prints what the calls returned and how many each
// recorded.
const providerErgoSrc = `import strconv
import sync

type Rec[A, G] {
	calls sync.Ref[[]A],
	answer sync.Ref[Option[G]],
}

func NewRec[A, G]() Rec[A, G] {
	Rec[A, G]{calls: sync.NewRef[[]A]([]A{}), answer: sync.NewRef[Option[G]](None)}
}

func Answer[A, G](r Rec[A, G], f G) {
	r.answer.Set(Some{f})
}

func Calls[A, G](r Rec[A, G]) []A {
	r.calls.Get()
}

func Call[A, G](r Rec[A, G], a A) Option[G] {
	r.calls.Update(func(v []A) []A { v.Append(a) })
	r.answer.Get()
}

type Empty {}

type Mock[S] derive Provider(Of[S](), Of[Rec](), Of[Empty]())

type Gone { Key string }

type Store {
	Size() int,
	Get(k string) int,
	Pair(a int, b int) int,
	Drop(k string) fails Gone,
	Tag(k string),
}

func main() needs Console {
	let m = Mock[Store]{}
	m.Size.Answer(func() int { 2 })
	m.Get.Answer(func(k string) int { len(k) })
	m.Pair.Answer(func(a int, b int) int { a * b })
	m.Drop.Answer(func(k string) fails Gone { Gone{Key: k} })
	m.Tag.Answer(func(k string) {})
	let n = with context { Store: m } {
		Store.Tag("t")
		Store.Size() + Store.Get("abc") + Store.Pair(4, 5)
	}
	let mut dropped = ""
	with context { Store: m } { Store.Drop("x") } catch {
	case Gone{Key: k}:
		dropped = k
	}
	Console.Println(strconv.Itoa(n))
	Console.Println(dropped)
	Console.Println(strconv.Itoa(len(m.Size.Calls()) + len(m.Get.Calls()) + len(m.Pair.Calls()) + len(m.Drop.Calls())))
	Console.Println(m.Get.Calls()[0] + m.Tag.Calls()[0])
}
`

// TestProviderErgoFieldTypechecks: a provider whose field type is declared
// in Ergo lowers to Go that type-checks: each field made by NewRec at its
// operation's arguments and signature, each operation answered through
// Call and the Option it returns (script.go providerCall). Mutant: the
// type arguments of the NewRec call dropped (provider-call-typeargs-dropped:
// Go cannot infer them, since NewRec has no parameters).
func TestProviderErgoFieldTypechecks(t *testing.T) {
	typecheckGo(t, []*lowered{mustLower(t, "providerergo", providerErgoSrc)})
}

// TestProviderErgoFieldAnswers: the operations of a provider with an Ergo
// field type answer with the test's answers and record their calls, a
// failure included. Mutant: the Option that Call returns never read, so
// every operation is unanswered (provider-answer-ignored).
//
// It builds Go because what the adapter answers is the run's output: the
// lowered text and its Go types show a call of Call, and only the run shows
// that its Some reaches the answer.
func TestProviderErgoFieldAnswers(t *testing.T) {
	needsBuild(t)
	exes := buildAll(t, []*lowered{mustLower(t, "providerergo", providerErgoSrc)}, true)
	if exes == nil {
		t.Fatal("the provider program does not build")
	}
	out, stderr, code := runExe(t, exes["providerergo"])
	if want := "25\nx\n4\nabct\n"; code != 0 || out != want {
		t.Fatalf("exit %d, stdout %q, stderr %s; want %q", code, out, stderr, want)
	}
}
