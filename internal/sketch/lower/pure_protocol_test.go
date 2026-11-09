package lower

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

// goProtocols is the test's own oracle of Go's method protocols, apart from
// the lowering's table (hostProtocols): error, fmt.Stringer and
// fmt.GoStringer, and the canonical methods of go vet's stdmethods check
// (golang.org/x/tools/go/analysis/passes/stdmethods, Go 1.27), with each
// parameter's and result's Go spelling; "=" marks a parameter vet keys on.
var goProtocols = map[string]struct{ args, results []string }{
	"Error": {nil, []string{"string"}}, "String": {nil, []string{"string"}}, "GoString": {nil, []string{"string"}},
	"As": {[]string{"any"}, []string{"bool"}}, "Format": {[]string{"=fmt.State", "rune"}, nil},
	"GobDecode": {[]string{"[]byte"}, []string{"error"}}, "GobEncode": {nil, []string{"[]byte", "error"}},
	"Is": {[]string{"error"}, []string{"bool"}}, "MarshalJSON": {nil, []string{"[]byte", "error"}},
	"MarshalXML": {[]string{"*xml.Encoder", "xml.StartElement"}, []string{"error"}},
	"ReadByte":   {nil, []string{"byte", "error"}}, "ReadFrom": {[]string{"=io.Reader"}, []string{"int64", "error"}},
	"ReadRune": {nil, []string{"rune", "int", "error"}}, "Scan": {[]string{"=fmt.ScanState", "rune"}, []string{"error"}},
	"Seek": {[]string{"=int64", "int"}, []string{"int64", "error"}}, "UnmarshalJSON": {[]string{"[]byte"}, []string{"error"}},
	"UnmarshalXML": {[]string{"*xml.Decoder", "xml.StartElement"}, []string{"error"}},
	"UnreadByte":   {nil, []string{"error"}}, "UnreadRune": {nil, []string{"error"}}, "Unwrap": {nil, []string{"error"}},
	"WriteByte": {[]string{"byte"}, []string{"error"}}, "WriteTo": {[]string{"=io.Writer"}, []string{"int64", "error"}},
}

// TestPureMethodsMeetNoHostProtocolByAccident (Q-S5-6; S5abc r1 M1) is a
// property over {plain function, method on a local record} x {each Go
// protocol name (goProtocols), plus a control name} x {matching,
// non-matching Go signature}. Each program declares one pure subject and
// prints its result. The model:
//   - (a) a method whose Go name and signature meet the protocol exactly
//     keeps the environment and its name;
//   - (b) a method with a protocol's name and another signature is pure,
//     and its Go name is the name with an underscore, outside the table;
//   - (c) a plain function, and a method with the control name, is pure
//     and keeps its name.
//
// Every program vets (go vet's stdmethods check reads the table's names),
// builds and prints the model's output. A matching signature exists only
// for a protocol whose Go types an Ergo type lowers to (Error, String and
// GoString: no parameter, a string result); the other protocols need any,
// error, []byte or a package type, which no Ergo signature of a pure
// function lowers to, so their matching cell is empty.
func TestPureMethodsMeetNoHostProtocolByAccident(t *testing.T) {
	needsBuild(t)
	names := []string{"Total"} // the control: no protocol has it
	for name := range goProtocols {
		names = append(names, name)
	}
	sort.Strings(names)
	type model struct {
		name, subject, goName string
		env                   bool
	}
	var cases []behavior
	var models []model
	for _, name := range names {
		for _, kind := range []string{"plain", "method"} {
			for _, sig := range []string{"matching", "other"} {
				params, result, ok := protocolSignature(name, sig)
				if !ok {
					continue
				}
				src, stdout := protocolProgram(name, kind, params, result)
				m := model{name: fmt.Sprintf("proto_%s_%s_%s", name, kind, sig), subject: name, goName: name}
				_, isProtocol := goProtocols[name]
				switch {
				case kind == "method" && isProtocol && sig == "matching":
					m.subject, m.env = "Rec."+name, true
				case kind == "method" && isProtocol:
					m.subject, m.goName = "Rec."+name+"_", name+"_"
				case kind == "method":
					m.subject = "Rec." + name
				}
				cases = append(cases, behavior{name: m.name, src: src, stdout: stdout})
				models = append(models, m)
			}
		}
	}
	// Every name has its two "other" programs; the control, Error, String
	// and GoString have their two matching ones too.
	if want := 2*len(names) + 8; len(models) != want {
		t.Fatalf("the property has %d programs, want %d: a cell of the domain went missing", len(models), want)
	}
	built := runBehaviors(t, cases)
	for _, m := range models {
		o := built[m.name]
		if o == nil {
			continue
		}
		got := envParams(t, o.mainGo())
		env, ok := got[m.subject]
		if !ok {
			var have []string
			for k := range got {
				if strings.Contains(k, m.goName[:2]) {
					have = append(have, k)
				}
			}
			sort.Strings(have)
			t.Errorf("%s: no Go function %s (have %v)", m.name, m.subject, have)
			continue
		}
		if env != m.env {
			t.Errorf("%s: %s takes the environment %v, want %v", m.name, m.subject, env, m.env)
		}
		if _, isProtocol := goProtocols[m.goName]; strings.HasPrefix(m.subject, "Rec.") && isProtocol && !m.env {
			t.Errorf("%s: the pure method %s has a host protocol's Go name", m.name, m.subject)
		}
	}
}

// protocolSignature is the Ergo parameter types and result of the subject
// named name: for "matching", the protocol's own Go signature when every
// type of it is an Ergo type's lowering (ok is false otherwise); for
// "other", the protocol's parameters where Ergo can spell them (int
// elsewhere) and an int result, which no protocol has, so go vet's keyed
// parameters still match and its check would read the method.
func protocolSignature(name, sig string) ([]string, string, bool) {
	p, isProtocol := goProtocols[name]
	ergo := map[string]string{"string": "string", "int": "int", "int64": "int64", "bool": "bool"}
	if sig == "matching" {
		if !isProtocol {
			return nil, "string", true // the control, with String's shape
		}
		var params []string
		for _, a := range p.args {
			e, ok := ergo[strings.TrimPrefix(a, "=")]
			if !ok {
				return nil, "", false
			}
			params = append(params, e)
		}
		if len(p.results) != 1 {
			return nil, "", false
		}
		r, ok := ergo[p.results[0]]
		if !ok {
			return nil, "", false
		}
		return params, r, true
	}
	var params []string
	for _, a := range p.args {
		e, ok := ergo[strings.TrimPrefix(a, "=")]
		if !ok {
			e = "int"
		}
		params = append(params, e)
	}
	return params, "int", true
}

// protocolProgram is a program whose pure subject is named name, a plain
// function or a function on the local record Rec (196m), with the given
// parameter types and result, and what it prints.
func protocolProgram(name, kind string, params []string, result string) (string, string) {
	var decl, args []string
	if kind == "method" {
		decl = append(decl, "r Rec")
		args = append(args, `Rec{Tag: "ab"}`)
	}
	value := map[string]string{"string": `"x"`, "int": "3", "int64": "4", "bool": "true"}
	for i, p := range params {
		decl = append(decl, fmt.Sprintf("p%d %s", i, p))
		args = append(args, value[p])
	}
	body, stdout := `"s"`, "s"
	if result == "int" {
		body, stdout = "7", "7"
	}
	if kind == "method" {
		body, stdout = "r.Tag", "ab"
		if result == "int" {
			body, stdout = "len(r.Tag)", "2"
		}
	}
	src := "type Rec { Tag string }\n\n" +
		"func " + name + "(" + strings.Join(decl, ", ") + ") " + result + " { " + body + " }\n\n" +
		"func main() needs Console {\n\tConsole.Println(\"\\(" + name + "(" + strings.Join(args, ", ") + "))\")\n}\n"
	return src, stdout + "\n"
}
