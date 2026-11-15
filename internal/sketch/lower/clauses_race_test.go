package lower

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/std"
)

// The std callback property of ESC1 (lang audit B, F1). A std function
// whose Go body runs a callback on another task, at the same time as
// another run or after the call returned, must take it `escaping`, so the
// checker refuses a literal that writes its caller's let mut (types.md,
// Function values 2 and 3). The property generates, per seed, a callback
// for each std function of std.ArgClauses that a program can hand one to:
// it writes its caller's let mut int and Map, and may use a resource the
// caller owns. Each generated function is either refused by the checker,
// or it runs under Go's race detector with no race, no fatal error and no
// use of the resource after its release. A non-escaping entry must be
// admitted (the clause lets its callback write in place), and an escaping
// entry whose callback writes a captured let mut must be refused.

var clauseSeeds = flag.String("clauses.seeds", "1,2,3", "TestStdCallbackClausesRace: the seeds, comma separated")

// clauseTemplate is one std function as a generated program calls it.
// body is an Ergo function NAME over log, the resource's record of uses;
// CB is the callback's generated statements, R the resource's declaration
// (empty when the callback uses none). It declares the let mut n and Map
// seen that the callback writes, and writes both after the call.
type clauseTemplate struct {
	keys     []string // std.ArgClauses keys (Func.Param) it covers; the first is its callback
	needs    string   // the function's needs row
	imports  []string // the std packages its body names
	resource bool     // the callback may use the caller's resource
	body     string
}

const clauseRDecl = "let r = Res{log: log}"

var clauseTemplates = []clauseTemplate{
	{keys: []string{"scope.OnExit.body"}, imports: []string{"scope"}, body: `
	scope.OnExit() {
		CB
	}
	n = n + 1
	seen[-1] = n
	"\(n) \(len(seen))"`},
	{keys: []string{"slices.SortFunc.compare"}, imports: []string{"slices"}, resource: true, body: `
	R
	let out = slices.SortFunc([]int{5, 3, 9, 1, 7, 2}, func(a int, b int) int {
		CB
		a - b
	})
	n = n + 1
	seen[-1] = n
	"\(n) \(len(seen)) \(out[0])"`},
	{keys: []string{"slices.SortStableFunc.compare"}, imports: []string{"slices"}, resource: true, body: `
	R
	let out = slices.SortStableFunc([]int{5, 3, 9, 1, 7, 2}, func(a int, b int) int {
		CB
		a - b
	})
	n = n + 1
	seen[-1] = n
	"\(n) \(len(seen)) \(out[0])"`},
	{keys: []string{"sync.Modify.f"}, resource: true, body: `
	R
	let cell = sync.NewRef[int](0)
	let k = sync.Modify(cell, func(v int) (int, int) {
		CB
		(v, v + 1)
	})
	n = n + 1
	seen[-1] = n
	"\(n) \(len(seen)) \(k)"`},
	{keys: []string{"sync.Ref.Update.f"}, resource: true, body: `
	R
	let cell = sync.NewRef[int](0)
	cell.Update(func(v int) int {
		CB
		v + 1
	})
	n = n + 1
	seen[-1] = n
	"\(n) \(len(seen)) \(cell.Get())"`},
	{keys: []string{"timeout.After.f"}, imports: []string{"timeout"}, needs: "Scheduler", resource: true, body: `
	R
	let v = timeout.After(1000s, func() int {
		CB
		1
	}) catch {
	case timeout.Expired:
		0
	}
	n = n + 1
	seen[-1] = n
	"\(n) \(len(seen)) \(v)"`},
	{keys: []string{"tasks.Transform.f"}, imports: []string{"tasks"}, resource: true, body: `
	R
	let mut xs = []int{}
	for i in 0..256 {
		xs = xs.Append(i)
	}
	let out = tasks.Transform(xs, 16, func(x int) int {
		CB
		x
	})
	n = n + 1
	seen[-1] = n
	"\(n) \(len(seen)) \(len(out))"`},
	{keys: []string{"task.Go.body"}, imports: []string{"task"}, resource: true, body: `
	R
	let tk = task.Go(func() int {
		CB
		1
	})
	let v = tk.Wait()
	n = n + 1
	seen[-1] = n
	"\(n) \(len(seen)) \(v)"`},
	{keys: []string{"tasks.Go.body", "tasks.Go.set"}, imports: []string{"tasks"}, resource: true, body: `
	R
	let set = tasks.NewSet[noFail](tasks.FailFast)
	for _ in 0..8 {
		tasks.Go(set, func() {
			CB
		}) catch {
		case tasks.Closed:
		}
	}
	set.Wait() catch {
	case noFail:
	}
	n = n + 1
	seen[-1] = n
	"\(n) \(len(seen))"`},
	{keys: []string{"http.Net.SendInto.put"}, imports: []string{"http"}, resource: true, body: `
	R
	let h = http.InMemory(func(q http.Request) http.Response needs http.Streaming { http.Response{Status: 200, Header: []http.Field{}, Body: "ab"} })
	let req = http.Request{Method: "GET", Path: "/", Query: []http.Field{}, Header: []http.Field{}, Body: http.Buffered{Text: ""}}
	let status = with context { http.Net: h } {
		let resp = http.Net.SendInto(req, http.AnySuccess, func(part string) fails http.SinkFailed {
			CB
		}) catch {
		case http.RequestFailed, http.StatusRefused, http.SinkFailed:
			http.Response{Status: 0, Header: []http.Field{}, Body: ""}
		}
		resp.Status
	}
	n = n + 1
	seen[-1] = n
	"\(n) \(len(seen)) \(status)"`},
	{keys: []string{"actor.start.out"}, imports: []string{"actor"}, needs: "Scheduler", resource: true, body: `
	R
	let a = actor.StartMachine(Counter{}, 64, func(o int) {
		CB
	})
	for i in 0..256 {
		n = n + 1
		seen[0 - i - 1] = i
		a.Send(Beat) catch {
		case actor.Stopped:
		}
	}
	a.Stop()
	"\(n) \(len(seen))"`},
}

// clauseUnreached are the std.ArgClauses entries no generated program can
// hand a callback to, or that this property does not reach, each with the
// reason.
var clauseUnreached = map[string]string{
	"actor.recovering.recover":     "recovering is unexported; StartDurable and SuperviseDurable call it with std literals over their escaping recover",
	"actor.recovering.start":       "recovering is unexported; its start is a std literal over StartMachine or launch",
	"http.sendInto.accepts":        "sendInto is unexported; accepts is Client.SendInto's literal over Accepts, with no capture",
	"http.sendInto.put":            "sendInto is unexported; Client.SendInto passes its own put through, and reaching it needs a live server, which this property does not run",
	"http.fetchInto.accepts":       "fetchInto is unexported; accepts is Remote.SendURLInto's literal over Accepts, with no capture",
	"http.fetchInto.put":           "fetchInto is unexported; Remote.SendURLInto passes its put through, and reaching it needs a live origin, which this property does not run",
	"http.serve.l":                 "serve is unexported; a listener is Serve's own parameter, not a callback",
	"exec.waitFor.p":               "waitFor is unexported; a Running is WaitFor's own parameter, not a callback, and a live one needs Ambient",
	"http.serve.handle":            "serve is unexported; its handle is Serve's literal over Serve's escaping handle, and a server runs until stopped, which this property does not do",
	"http.IntoFile.fetch":          "IntoFile needs an fs.HostRoot, which needs Ambient; the generated main holds Console only",
	"http.Network.SendURLInto.put": "Network's std provider (NewNet's Remote) needs Ambient and a live origin; http.Net.SendInto.put's template covers the in-memory provider's shape",
	"rpc.exits.handle":             "exits is unexported; Answer passes its escaping handle through, and Answer needs http.Streaming",
	"rpc.exitLines.handle":         "exitLines is unexported; Answer passes its escaping handle through, and Answer needs http.Streaming",
	"rpc.exitLines.emit":           "exitLines is unexported; emit is Answer's literal over the stream's BodyWriter",
	"actor.keep.save":              "save is StartDurable's, an escaping parameter there; keep is called only by the durable step",
	"csv.scan.each":                "csv's own literals are its only callers; scan is unexported",
	"actor.feed.run":               "Feed's source is a Seq, escaping by its named function type; run is Feed's own literal",
	"actor.start.fresh":            "a std literal over the escaping m.Initial; StartMachine's user callback is sink (actor.start.out)",
	"actor.start.step":             "a Machine field, escaping by its position (types.md, Function values 2)",
	"actor.start.fire":             "a std literal over the Machine's fields",
	"actor.start.delay":            "a std literal over the Machine's fields",
	"actor.start.workOf":           "a Machine field, escaping by its position",
	"actor.start.hasWork":          "a std literal with no capture",
	"actor.start.work":             "a std literal over a Machine field's work",
	"actor.start.kind":             "a std literal with no capture",
	"actor.start.next":             "a std literal with no capture",
	"actor.child.step":             "a Machine field, escaping by its position",
	"actor.child.fire":             "a std literal over the Machine's fields",
	"actor.child.delay":            "a std literal over the Machine's fields",
	"actor.child.workOf":           "a Machine field, escaping by its position",
	"actor.child.hasWork":          "a std literal with no capture",
	"actor.child.work":             "a std literal over a Machine field's work",
	"actor.child.kind":             "a std literal with no capture",
	"actor.child.next":             "a std literal with no capture",
	"actor.child.out":              "Child's toParent reaches it; Child runs only in a machine's work with its send (S108 elsewhere), and toParent is escaping in Child's own signature",
}

const clauseHeader = `
// Res is the generated programs' resource: its log records each use and
// its release, so a use after the release shows.
type Res {
	log sync.Ref[[]string],
}

func Use(r Res) {
	r.log.Update(func(xs []string) []string { xs.Append("use") })
}

func Close(r Res) {
	r.log.Update(func(xs []string) []string { xs.Append("close") })
}

type noFail {}

type Tick = Beat{}

type Count = At{ N int }

type Counter machine[Count, Tick] {
	initial: At{N: 0},
	step:    counterStep,
}

func counterStep(s Count, t Tick) Decision[Count, int] {
	match s {
	case At as a:
		match t {
		case Beat:
			Emit{Next: At{N: a.N + 1}, Output: a.N + 1}
		}
	}
}

// late counts the uses after a release in log.
func late(log sync.Ref[[]string]) int {
	let mut closed = false
	let mut k = 0
	for x in log.Get() {
		if x == "close" {
			closed = true
		} else if closed {
			k = k + 1
		}
	}
	k
}
`

// clauseCase is one generated function: a template with a callback.
type clauseCase struct {
	name   string
	tmpl   clauseTemplate
	writes bool // the callback writes a captured let mut
	src    string
}

// genClauseCase draws the callback of tmpl for seed: one to three
// statements, each an increment of n, a write of seen at a drawn key, or,
// when the template allows it, a use of the caller's resource.
func genClauseCase(seed uint64, i int, tmpl clauseTemplate) clauseCase {
	rng := rand.New(rand.NewPCG(seed, uint64(i)))
	c := clauseCase{name: fmt.Sprintf("s%dc%d", seed, i), tmpl: tmpl}
	var stmts []string
	usesR := false
	for range 1 + rng.IntN(3) {
		k := 3
		if tmpl.resource {
			k = 4
		}
		switch rng.IntN(k) {
		case 0, 1:
			stmts = append(stmts, "n = n + 1")
			c.writes = true
		case 2:
			stmts = append(stmts, fmt.Sprintf("seen[%d] = n", rng.IntN(64)))
			c.writes = true
		case 3:
			stmts = append(stmts, "r.Use()")
			usesR = true
		}
	}
	indent := "\t\t"
	if strings.Contains(tmpl.body, "\t\t\tCB") {
		indent = "\t\t\t"
	}
	body := strings.Replace(tmpl.body, "CB", strings.Join(stmts, "\n"+indent), 1)
	rdecl := ""
	if usesR {
		rdecl = clauseRDecl
	}
	if rdecl != "" {
		rdecl = "\t" + rdecl + "\n"
	}
	body = strings.Replace(body, "\tR\n", rdecl, 1)
	needs := ""
	if tmpl.needs != "" {
		needs = " needs " + tmpl.needs
	}
	c.src = fmt.Sprintf("// %s calls %s.\nfunc %s(log sync.Ref[[]string]) string%s {\n\tlet mut n = 0\n\tlet mut seen = Map[int, int]{}%s\n}\n", c.name, tmpl.keys[0], c.name, needs, body)
	return c
}

// clauseProgram is the header, the cases and a main that runs each case
// and prints its result and its uses after release.
func clauseProgram(cases []clauseCase) string {
	imports := []string{"sync", "time"}
	for _, c := range cases {
		for _, p := range c.tmpl.imports {
			if !slices.Contains(imports, p) {
				imports = append(imports, p)
			}
		}
	}
	slices.Sort(imports)
	var b strings.Builder
	for _, p := range imports {
		b.WriteString("import " + p + "\n")
	}
	b.WriteString(clauseHeader)
	for _, c := range cases {
		b.WriteString("\n" + c.src)
	}
	b.WriteString("\nfunc main() needs Console {\n\twith context { Scheduler: time.NewManual(0) } {\n")
	for _, c := range cases {
		fmt.Fprintf(&b, "\t\tlet log%s = sync.NewRef[[]string]([]string{})\n\t\tConsole.Println(\"%s \\(%s(log%s)) late=\\(late(log%s))\")\n", c.name, c.name, c.name, c.name, c.name)
	}
	b.WriteString("\t}\n}\n")
	return b.String()
}

// TestStdCallbackClausesAdmit is the property's in-process half, in every
// tier: each template covers its std.ArgClauses entries, a non-escaping
// entry admits a callback that writes in place, and an escaping entry
// refuses one by the capture rule.
func TestStdCallbackClausesAdmit(t *testing.T) {
	clauseAdmitted(t)
}

// clauseAdmitted checks the generated cases of the seeds in process and
// returns the admitted ones, with the seeds.
func clauseAdmitted(t *testing.T) ([]clauseCase, []uint64) {
	t.Helper()
	covered := map[string]bool{}
	for _, tmpl := range clauseTemplates {
		for _, k := range tmpl.keys {
			covered[k] = true
		}
	}
	clauseOf := map[string]string{}
	for _, c := range std.ArgClauses {
		key := c.Func + "." + c.Param
		clauseOf[key] = c.Clause
		if !covered[key] && clauseUnreached[key] == "" {
			t.Errorf("std.ArgClauses lists %s, which no clause template calls and clauseUnreached does not name", key)
		}
		if covered[key] && clauseUnreached[key] != "" {
			t.Errorf("%s is both a clause template and unreached", key)
		}
	}
	for _, tmpl := range clauseTemplates {
		for _, k := range tmpl.keys {
			if _, ok := clauseOf[k]; !ok {
				t.Errorf("clause template %s names no std.ArgClauses entry", k)
			}
		}
	}
	var seeds []uint64
	for _, s := range strings.Split(*clauseSeeds, ",") {
		n, err := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
		if err != nil {
			t.Fatalf("-clauses.seeds: %v", err)
		}
		seeds = append(seeds, n)
	}
	var admitted []clauseCase
	for _, seed := range seeds {
		for i, tmpl := range clauseTemplates {
			c := genClauseCase(seed, i, tmpl)
			o := lowerSource(c.name, clauseProgram([]clauseCase{c}), true)
			ok := o.err == nil && len(o.diags) == 0 && o.panicked == nil && len(o.refused) == 0
			escaping := clauseOf[tmpl.keys[0]] == "escaping"
			// A refusal counts only by a capture rule (types.md, Function
			// values 3; resources R31 to R33): S108. Any other outcome is
			// a defect of the template, not a verdict.
			capture, other := false, o.err != nil || o.panicked != nil || len(o.refused) > 0
			for _, d := range o.diags {
				switch {
				case d.Code == "S108" && (strings.Contains(d.Message, "is a let mut of an enclosing frame") || strings.Contains(d.Message, "is written here after a function literal")):
					capture = true
				case d.Code == "S108":
				default:
					other = true
				}
			}
			replay := fmt.Sprintf("replay: go test -run TestStdCallbackClauses ./internal/sketch/lower -args -clauses.seeds=%d", seed)
			switch {
			case other:
				t.Errorf("%s (seed %d): the template of %s does not check: err %v, diagnostics %v, refused %+v, panic %v\n%s\n%s", c.name, seed, tmpl.keys[0], o.err, o.diags, o.refused, o.panicked, c.src, replay)
			case !escaping && !ok:
				t.Errorf("%s (seed %d): %s is non-escaping, so a callback that writes in place is admitted, but: diagnostics %v\n%s\n%s", c.name, seed, tmpl.keys[0], o.diags, c.src, replay)
			case escaping && c.writes && !capture:
				t.Errorf("%s (seed %d): %s is escaping, and a callback that writes its caller's let mut was not refused by the capture rule (types.md, Function values 3): diagnostics %v\n%s\n%s", c.name, seed, tmpl.keys[0], o.diags, c.src, replay)
			case ok:
				admitted = append(admitted, c)
			}
		}
	}
	return admitted, seeds
}

// TestStdCallbackClausesRace is the property's run: the admitted cases of
// every seed, in one program built with -race.
//
// It builds Go because the race it guards (lang audit B, F1: two tasks
// writing one Map) is seen only by Go's race detector in a built binary,
// or as Go's fatal "concurrent map writes"; no in-process check observes
// which task runs a callback, or when.
func TestStdCallbackClausesRace(t *testing.T) {
	needsBuild(t)
	admitted, seeds := clauseAdmitted(t)
	if t.Failed() {
		return
	}
	if len(admitted) == 0 {
		t.Fatal("no generated case was admitted")
	}
	src := clauseProgram(admitted)
	o := mustLower(t, "clauses", src)
	stdout, stderr, code := runRaceProgram(t, o)
	if code != 0 || strings.Contains(stderr, "DATA RACE") || strings.Contains(stderr, "fatal error") || strings.Contains(stdout, "late=") && !allLateZero(stdout) {
		t.Errorf("the admitted callbacks of seeds %v ran unsafely (exit %d)\nstdout:\n%s\nstderr:\n%s\nsource:\n%s\nreplay: go test -run TestStdCallbackClausesRace ./internal/sketch/lower -args -clauses.seeds=%s", seeds, code, stdout, firstLines(stderr, 60), src, *clauseSeeds)
	}
	if got := strings.Count(stdout, "late="); got != len(admitted) {
		t.Errorf("%d of %d admitted cases printed their result\nstdout:\n%s", got, len(admitted), stdout)
	}
}

// allLateZero reports whether every case line of out ends late=0.
func allLateZero(out string) bool {
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if !strings.HasSuffix(line, " late=0") {
			return false
		}
	}
	return true
}

// runRaceProgram builds o with -race and runs it once with four threads,
// under the test kit's memory limit (runProgram).
//
// It builds Go because Go's race detector exists only in a built binary.
func runRaceProgram(t *testing.T, o *lowered) (string, string, int) {
	t.Helper()
	m := newGoModuleDir(t)
	m.program(o, o.mainGo())
	exe := filepath.Join(m.root, "race")
	build := exec.Command("go", "build", "-race", "-trimpath", "-o", exe, "./p/"+o.name)
	build.Dir = m.root
	build.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off", "CGO_ENABLED=1")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build -race: %v\n%s", err, firstLines(string(out), 40))
	}
	run := exec.Command(exe)
	run.Env = append(os.Environ(), "GORACE=halt_on_error=1") // inherit the worker's CPU share
	var stdout, stderr bytes.Buffer
	run.Stdout, run.Stderr = &stdout, &stderr
	err := runProgram(t, run)
	code := 0
	if err != nil {
		code = -1
		var x *exec.ExitError
		if errors.As(err, &x) {
			code = x.ExitCode()
		}
	}
	return stdout.String(), stderr.String(), code
}
