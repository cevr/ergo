package residue_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

var (
	// workClassNames is the class list of work merge-file, as
	// modules/work/classes declares it.
	workClassNames = regexp.MustCompile(`(?m)^let ClassNames = \[\]string\{([^}]*)\}`)
	// workDriverLine is a driver setting of work's DriverSettings: the
	// key's suffix and the value, with \(c) for the class.
	workDriverLine = regexp.MustCompile(`"merge\.ergo-\\\(c\)\.(name|driver)", "([^"]*)"`)
	// headerClasses is the class loop of the .gitattributes header.
	headerClasses = regexp.MustCompile(`(?m)^#\s+for c in ([a-z ]+); do$`)
	// headerDriverLine is a driver setting of that loop, with $c for the
	// class.
	headerDriverLine = regexp.MustCompile(`(?m)^#\s+git config merge\.ergo-\$c\.(name|driver) "([^"]*)"$`)
	// attributesDriver is a merge=ergo-CLASS attribute of a pattern line.
	attributesDriver = regexp.MustCompile(`(?m)^[^#\s]\S*\s.*\bmerge=ergo-([a-z]+)\b`)
)

// TestDriverTableIsWorks is the guard of DRIVER-TABLE-ONE-SOURCE: this
// repository's merge-driver table exists twice. work writes the drivers
// from classes.ClassNames and DriverSettings
// (modules/work/work/merge.ergo); lanework.sh drivers and a person by
// hand write them from the table in the header of .gitattributes. The two
// must name the same classes with the same settings, and every
// merge=ergo-CLASS line must name a class work merges. Otherwise a clone
// fixed one way merges differently from a clone fixed the other way, or
// Git hands a file to a driver no checkout has, and the file merges as
// text without a word.
func TestDriverTableIsWorks(t *testing.T) {
	root, err := exec.Command("git", "--no-optional-locks", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	repo := strings.TrimSpace(string(root))
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(repo, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	classesSrc := read("modules/work/classes/classes.ergo")
	mergeSrc := read("modules/work/work/merge.ergo")
	attrs := read(".gitattributes")

	m := workClassNames.FindStringSubmatch(classesSrc)
	if m == nil {
		t.Fatal("modules/work/classes/classes.ergo declares no `let ClassNames = []string{...}`")
	}
	var work []string
	for _, f := range strings.Split(m[1], ",") {
		if s := strings.Trim(strings.TrimSpace(f), `"`); s != "" {
			work = append(work, s)
		}
	}
	h := headerClasses.FindStringSubmatch(attrs)
	if h == nil {
		t.Fatal(".gitattributes has no `#   for c in CLASSES; do` line in its driver table")
	}
	header := strings.Fields(h[1])
	if !slices.Equal(header, work) {
		t.Errorf(".gitattributes's driver table names the classes %v, work's classes.ClassNames %v: make the header's loop name work's", header, work)
	}

	settings := func(re *regexp.Regexp, src, class string) map[string]string {
		out := map[string]string{}
		for _, s := range re.FindAllStringSubmatch(src, -1) {
			out[s[1]] = strings.ReplaceAll(s[2], class, "CLASS")
		}
		return out
	}
	want := settings(workDriverLine, mergeSrc, `\(c)`)
	got := settings(headerDriverLine, attrs, "$c")
	if len(want) != 2 {
		t.Fatalf("merge.ergo's DriverSettings gives %d ergo-CLASS settings, want name and driver: %v", len(want), want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf(".gitattributes's driver table sets merge.ergo-$c.%s to %q, work's DriverSettings to %q", k, got[k], v)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf(".gitattributes's driver table sets merge.ergo-$c.%s, which work does not write", k)
		}
	}

	for _, a := range attributesDriver.FindAllStringSubmatch(attrs, -1) {
		if !slices.Contains(work, a[1]) {
			t.Errorf(".gitattributes gives a file merge=ergo-%s, a class work merge-file does not have (%v)", a[1], work)
		}
	}
}
