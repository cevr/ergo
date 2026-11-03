package proto

import (
	"fmt"
	gotoken "go/token"
	"go/types"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

func TestReviewNamePositions(t *testing.T) {
	positions := []struct{ name, body string }{
		{"type", `type NAME {}; func main(){}`},
		{"function", `func NAME(){}; func main(){}`},
		{"field", `type Box { NAME string }; func main(){}`},
		{"method", `type Box {}; func NAME(_ Box){}; func main(){}`},
		{"capability-method", `type Cap { NAME() }; func main(){}`},
		{"context", `let NAME = context {}; func main(){}`},
		{"parameter", `func f(NAME string){}; func main(){}`},
		{"receiver", `type Box {}; func F(NAME Box){}; func main(){}`},
		{"local", `func main() needs Console{ let NAME = "ok"; Console.Println(NAME) }`},
		{"switch", `type A { Value string }; type U = A; func makeU() U { return A{} }; func main() needs Console { match makeU() { case A as NAME: Console.Println(NAME.Value) } }`},
	}
	names := append([]string{}, types.Universe.Names()...)
	for kind := gotoken.BREAK; kind <= gotoken.VAR; kind++ {
		names = append(names, kind.String())
	}
	names = append(names, syntax.Keywords()...)
	names = append(names, "rt", "fmt", "time", "init", "Error", "v__1", "e__2", "envc__3", "env__", "t__", "Env__", "e__", "plain__", "is__U", "context__W", "main__")
	for _, position := range positions {
		for _, name := range names {
			t.Run(position.name+"/"+name, func(t *testing.T) {
				src := "" + strings.ReplaceAll(position.body, "NAME", name)
				shouldReject := position.name != "field" || syntax.IsKeyword(name) || name == "Error" || strings.Contains(name, "__")
				file, err := Parse("name.ergo", src)
				rejected := err != nil
				var ds []Diagnostic
				if err == nil {
					_, ds = Check(file)
					rejected = len(ds) > 0
				}
				if rejected != shouldReject {
					t.Errorf("reject=%v want=%v parse=%v diagnostics=%v source=%s", rejected, shouldReject, err, ds, src)
				}
			})
		}
	}
}

func TestReviewSafeNameScopes(t *testing.T) {
	positions := []string{
		`type NAME {}; func main() needs Console { Console.Println("ok") }`,
		`func NAME() string { return "ok" }; func main() needs Console { Console.Println(NAME()) }`,
		`type Box { NAME string }; func main() needs Console { Console.Println(Box{NAME: "ok"}.NAME) }`,
		`type Cap { NAME() string }; type Box {}; func NAME(_ Box) string{return "ok"}; func main() needs Console { with context {Cap: Box{}} {Console.Println(Cap.NAME())} }`,
		`let NAME = context {}; func main() needs Console { with NAME {Console.Println("ok")} }`,
		`func f(NAME string) string{return NAME}; func main() needs Console{Console.Println(f("ok"))}`,
		`type Box {Value string}; type Cap {Get() string}; func Get(NAME Box) string{return NAME.Value}; func main() needs Console{with context {Cap: Box{Value:"ok"}} {Console.Println(Cap.Get())}}`,
		`func main() needs Console{let NAME = "ok";Console.Println(NAME)}`,
		`type A {Value string};type U = A;func makeU() U{return A{Value:"ok"}};func main() needs Console{match makeU() {case A as NAME:Console.Println(NAME.Value)}}`,
	}
	for _, name := range []string{"envc", "e", "plain", "Item", "v", "Env"} {
		for i, body := range positions {
			t.Run(fmt.Sprintf("%s/%d", name, i), func(t *testing.T) {
				if got := buildRun(t, ""+strings.ReplaceAll(body, "NAME", name)); got != "ok\n" {
					t.Fatalf("got %q", got)
				}
			})
		}
	}
}
