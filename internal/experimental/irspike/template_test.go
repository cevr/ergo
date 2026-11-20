package irspike

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/toolchain"
)

// templatePage is WEB0's probe page (examples of the web-ssr probe): a
// page of static markup around a title, a list of stars and a list of
// posts, each a component with data in its texts, attributes and links.
const templatePage = `import html

type Post { Slug string, Title string, Date string }

type Star { Left string, Size string }

func star(s Star) html.Node {
	html.Div([]html.Child{html.Class("animate-twinkle absolute"), html.StyleAttr("left:" + s.Left + ";width:" + s.Size)})
}

func post(p Post) html.Node {
	html.Li([]html.Child{html.A([]html.Child{
		html.Class("group block"),
		html.Href(html.LocalURL("/blog/" + p.Slug)),
		html.Time([]html.Child{html.Class("font-mono"), html.Datetime(p.Date), html.Text(p.Date)}),
		html.H2([]html.Child{html.Class("text-lg"), html.Text(p.Title)}),
	})})
}

func page(title string, ss []Star, ps []Post) html.Node {
	html.Doc(html.Html([]html.Child{
		html.Lang("en"),
		html.Head([]html.Child{html.Meta([]html.Attr{html.Charset("utf-8")}), html.Title([]html.Child{html.Text(title)})}),
		html.Body([]html.Child{html.Div([]html.Child{html.Class("pointer-events-none"), html.Each(ss, star)}), html.H1([]html.Child{html.Text(title)}), html.Ul([]html.Child{html.Each(ps, post)})}),
	}))
}

func main() needs Console {
	let mut ps = []Post{}
	for i in 0..3 {
		ps = ps.Append(Post{Slug: "p\(i)", Title: "<t> & \(i)", Date: "2026-11-\(10 + i)"})
	}
	Console.Println(html.Render(page("w", []Star{Star{Left: "\(len(ps))%", Size: "2px"}}, ps)))
}
`

// TestTemplateCounts is the template pass's selection and switch, in
// process (CF2): on a build whose Go and whose compiler's Go are the
// pinned release, WEB0's page, post and star are each one template, so
// their IR bodies call no element function (a hole read inside an html
// function widens until the run ends: LocalURL's argument to the Href,
// whose attribute is a hole; Each's list to the Each). The switch off (CF1's form), a build by
// another Go release and the fold off build none.
func TestTemplateCounts(t *testing.T) {
	defer lower.SetConstFoldBuildGoForTest(toolchain.Version)()
	defer lower.SetConstFoldCompilerGoForTest(toolchain.Version)()
	file := filepath.Join(t.TempDir(), "p.ergo")
	if err := os.WriteFile(file, []byte(templatePage), 0o644); err != nil {
		t.Fatal(err)
	}
	u, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	files, rep := lowerFold(t, u, "")
	if rep.Folds.Templates < 3 {
		t.Errorf("templates %+v: want page, post and star each one", rep.Folds)
	}
	for _, fn := range []string{"page", "Post.post", "Star.star"} {
		body, ok := foldIRBody(t, files, fn)
		if !ok {
			t.Errorf("%s takes no IR body (fallback %v)", fn, rep.Fallback)
			continue
		}
		for _, el := range []string{`"name":"Div"`, `"name":"Li"`, `"name":"Html"`, `"name":"Text"`} {
			if strings.Contains(body, el) {
				t.Errorf("%s is no template: it calls %s\n%s", fn, el, body)
			}
		}
	}
	prev := lower.SetConstTemplateLoweringForTest("off")
	_, off := lowerFold(t, u, "")
	lower.SetConstTemplateLoweringForTest(prev)
	if off.Folds.Templates != 0 || off.Folds.TemplateRefused != 0 {
		t.Errorf("templates off: %+v, want none", off.Folds)
	}
	restore := lower.SetConstFoldBuildGoForTest("go1.26.0")
	_, other := lowerFold(t, u, "")
	restore()
	if other.Folds != (lower.FoldCounts{}) {
		t.Errorf("built by go1.26.0: %+v, want none", other.Folds)
	}
	if _, none := lowerFold(t, u, "off"); none.Folds != (lower.FoldCounts{}) {
		t.Errorf("the fold off: %+v, want none", none.Folds)
	}
}

// qualifiedNames names another package's record with no fields
// (base64.StdEncoding), its let lowered as a Go constant (io.DefaultChunk)
// and its let held in a Go variable (code.RenameNames).
const qualifiedNames = `import base64
import code
import io

func enc() base64.StdEncoding { base64.StdEncoding }

func chunk() int { io.DefaultChunk }

func names() int { len(code.RenameNames) }

func main() needs Console {
	let _ = enc()
	Console.Println("\(chunk() + names())")
}
`

// TestIRReadsQualifiedNames is the smallest repro of HTML-COST's IR gap:
// a body that names another package's record with no fields or its let
// (pkg.T, pkg.v) fell back to the text path ("selector on a non-record"),
// which kept the fluid gallery's page, gallery.View(gallery.ButtonDocs),
// out of the fold. Each such body takes IR.
func TestIRReadsQualifiedNames(t *testing.T) {
	file := filepath.Join(t.TempDir(), "p.ergo")
	if err := os.WriteFile(file, []byte(qualifiedNames), 0o644); err != nil {
		t.Fatal(err)
	}
	u, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	_, rep := lowerFold(t, u, "")
	for _, fn := range []string{"enc", "chunk", "names"} {
		if !slices.Contains(rep.IR, fn) {
			t.Errorf("%s takes no IR body (fallback %v)", fn, rep.Fallback)
		}
	}
}

// TestTemplateAgrees is CF2's law: a program whose pages are templates
// prints what the program built with CF1's form prints (the switch off,
// the reference), and both print what the source path (the Go text)
// prints, for generated pages that read run-time data in every place a
// page may, so a hole's escaping, its place and the order the holes run
// in (noisy prints) all show. Every seed builds a template. A failing
// seed prints the Ergo source, the outcomes and its replay command. It
// builds Go for the reason needsBuild gives: the question is what the
// built program prints.
func TestTemplateAgrees(t *testing.T) {
	src, ir := needsBuild(t)
	release, err := os.ReadFile(filepath.Join(ir.GOROOT, "VERSION"))
	if err != nil {
		t.Fatal(err)
	}
	v, _, _ := strings.Cut(string(release), "\n")
	defer lower.SetConstFoldBuildGoForTest(strings.Fields(v + " ")[0])()
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 6)
	for seed := first; seed < first+count; seed++ {
		dir := t.TempDir()
		text := GenerateTemplate(seed)
		file := filepath.Join(dir, "p.ergo")
		if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		u, err := Load(file)
		if err != nil {
			t.Fatalf("seed %d: the generated program does not check: %v\n%s", seed, err, text)
		}
		replay := "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run TestTemplateAgrees ./internal/experimental/irspike"
		var outs [2]outcome
		for i, mode := range []string{"", "off"} {
			prev := lower.SetConstTemplateLoweringForTest(mode)
			rep, err := EmitFold(u, filepath.Join(dir, "ir"+mode), "") // the fold on: the template switch alone varies
			lower.SetConstTemplateLoweringForTest(prev)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "" && rep.Folds.Templates == 0 {
				t.Fatalf("seed %d: no template (folds %+v, IR %v, fallback %v)\n%s\n%s", seed, rep.Folds, rep.IR, rep.Fallback, replay, text)
			}
			bin := filepath.Join(dir, "ir"+mode+".bin")
			if _, err := Build(ir, IR, filepath.Join(dir, "ir"+mode), bin, BuildOptions{}); err != nil {
				t.Fatalf("seed %d: %v\n%s\n%s", seed, err, replay, text)
			}
			outs[i] = run(t, bin)
		}
		if _, err := Emit(u, Source, filepath.Join(dir, "source")); err != nil {
			t.Fatal(err)
		}
		sbin := filepath.Join(dir, "source.bin")
		if _, err := Build(src, Source, filepath.Join(dir, "source"), sbin, BuildOptions{}); err != nil {
			t.Fatalf("seed %d: %v\n%s", seed, err, text)
		}
		source := run(t, sbin)
		if outs[0] != outs[1] || outs[1] != source || outs[0].exit != 0 {
			t.Fatalf("seed %d: the template, the reference and the source path differ, or the program failed\ntemplate:  %+v\nreference: %+v\nsource:    %+v\n%s\n%s", seed, outs[0], outs[1], source, replay, text)
		}
	}
}
