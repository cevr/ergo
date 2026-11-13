package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// start counts what the ergo process costs before and around a warm answer
// (compile-speed plan §3.2, item 5): the thin client, `ergo version`, the
// warm `ergo check` and `ergo build` of a module of four inputs, and the
// start of a probe binary for each of ergo's own packages, which imports
// that package and nothing else (its init and its share of the runtime's
// start), against a Go program with an empty main. Each process runs
// startRuns times; a line shows the least count. The probes are built
// with go build -overlay, so nothing is written into the tree.
func (r runner) start() int {
	r.leastOf("thin-client ergo (usage, no work)", []string{r.ergo})
	r.leastOf("ergo version", []string{r.ergo, "version"})

	mod := filepath.Join(r.work, "warm")
	for name, text := range map[string]string{
		"ergo.mod":          "module warm\n",
		"lib/lib.ergo":      "// Package lib is the start receipt's library.\n\nlet Banner = embedded[string](\"banner.txt\")\n\n// Value is the library's number.\nfunc Value() int {\n\t1\n}\n",
		"lib/banner.txt":    "banner a\n",
		"cmd/app/main.ergo": "import io\nimport os\nimport warm/lib\n\nfunc main() fails os.Exit needs Ambient {\n\twith context { io.Stdout: io.NewHost() } {\n\t\tio.Stdout.Println(\"\\(lib.Banner) \\(lib.Value())\")\n\t}\n}\n",
	} {
		path := filepath.Join(mod, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	xdg := filepath.Join(r.work, "xdg-start")
	out := filepath.Join(r.work, "app")
	app := filepath.Join(mod, "cmd", "app")
	for _, args := range [][]string{{"check", app}, {"build", app, "-o", out}} {
		r.counted("first "+args[0], xdg, args...)
		r.counted("warm "+args[0], xdg, args...)
		r.leastOfIn("warm "+args[0]+" (least)", mod, xdg, append([]string{r.ergo}, args...))
	}

	pkgs, err := ownPackages()
	if err != nil {
		fmt.Fprintln(os.Stderr, "buildcost start:", err)
		return 1
	}
	probes := filepath.Join(r.work, "probes")
	if err := os.MkdirAll(probes, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	base := r.probe(root, probes, "")
	if base == "" {
		return 1
	}
	r.leastOf("probe: empty main", []string{base})
	// The standard packages whose init ergo pays, which the module probes
	// include: each is counted on its own as well.
	pkgs = append(pkgs, stdProbes...)
	for _, pkg := range pkgs {
		if bin := r.probe(root, probes, pkg); bin != "" {
			r.leastOf("probe: "+strings.TrimPrefix(pkg, modulePath+"/"), []string{bin})
			os.Remove(bin) // a probe is up to the size of ergo
		}
	}
	return 0
}

// stdProbes are the standard packages start counts on their own.
var stdProbes = []string{"crypto/tls", "crypto/x509", "encoding/json", "go/parser", "go/types", "net/http", "regexp", "text/template"}

// startRuns is how many times start runs each process; it shows the least.
const startRuns = 5

// modulePath is the repository's Go module.
const modulePath = "ergo.local/prototype"

// ownPackages are the module's packages cmd/ergo links, in import path
// order.
func ownPackages() ([]string, error) {
	out, err := exec.Command("go", "list", "-deps", "./cmd/ergo").Output()
	if err != nil {
		return nil, err
	}
	var pkgs []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.HasPrefix(line, modulePath+"/") && line != modulePath+"/cmd/ergo" {
			pkgs = append(pkgs, line)
		}
	}
	slices.Sort(pkgs)
	return pkgs, nil
}

// probe builds a program whose main package imports pkg for its init (none
// for ""), placed in the module through an overlay, and returns its path,
// or "" when it does not build.
func (r runner) probe(root, dir, pkg string) string {
	name := "empty"
	imports := ""
	if pkg != "" {
		name = "p_" + strings.ReplaceAll(strings.TrimPrefix(pkg, modulePath+"/"), "/", "_")
		imports = fmt.Sprintf("import _ %q\n\n", pkg)
	}
	src := filepath.Join(dir, name+".go")
	if err := os.WriteFile(src, []byte("package main\n\n"+imports+"func main() {}\n"), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return ""
	}
	// The probe sits in a folder under internal/ that does not exist, so
	// it may import every internal package of the module.
	virtual := filepath.Join(root, "internal", "buildcostprobe", name, "main.go")
	overlay := filepath.Join(dir, name+".json")
	data, _ := json.Marshal(map[string]map[string]string{"Replace": {virtual: src}})
	if err := os.WriteFile(overlay, data, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return ""
	}
	bin := filepath.Join(dir, name)
	build := exec.Command("go", "build", "-overlay", overlay, "-o", bin, virtual)
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "buildcost: probe %s: %v\n", name, err)
		return ""
	}
	return bin
}

// leastOf runs argv startRuns times and prints the least instruction count.
func (r runner) leastOf(label string, argv []string) {
	r.leastOfIn(label, "", "", argv)
}

func (r runner) leastOfIn(label, dir, xdg string, argv []string) {
	var least result
	for i := range startRuns {
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Dir = dir
		if xdg != "" {
			cmd.Env = append(os.Environ(), "XDG_CACHE_HOME="+xdg)
		}
		res := measure(cmd)
		if i == 0 || res.instructions < least.instructions {
			least = res
		}
	}
	fmt.Printf("%-44s exit=%d instr_M=%.2f | user_s=%.3f sys_s=%.3f wall_ms=%.2f\n", label, least.exit, float64(least.instructions)/1e6, least.user.Seconds(), least.sys.Seconds(), float64(least.wall.Microseconds())/1000)
}
