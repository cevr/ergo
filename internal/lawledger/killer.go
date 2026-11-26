package lawledger

import (
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// stdDir is std's source folder, from the module root.
const stdDir = "internal/sketch/std"

var importLine = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`(?m)^import\s+(.+?)\s*$`) })

// LawDeclared reports whether the law a killer `law:NAME` names is declared
// where the package folder dir (from the module root root) runs it. NAME is
// L, a law of dir; T.L, a member law of dir's type T; or pkg.T.L, a member
// law of the operation type T that the package dir imports as pkg declares,
// which dir's providers run ("law workflow.Store.X for Store" in
// std/workflow/memory: LAW-RUNS-CREDIT-PROVIDERS). The law is found by its
// own name, L, in the .ergo files of dir or, for pkg.T.L, of the imported
// package.
func LawDeclared(root, dir, name string) bool {
	parts := strings.Split(name, ".")
	law := parts[len(parts)-1]
	dirs := []string{filepath.Join(root, filepath.FromSlash(dir))}
	if len(parts) == 3 {
		dirs = importedDirs(root, dirs[0], parts[0])
	}
	decl := regexp.MustCompile(`(?m)^\s*law ` + regexp.QuoteMeta(law) + `\b`)
	for _, d := range dirs {
		files, _ := filepath.Glob(filepath.Join(d, "*.ergo"))
		for _, f := range files {
			if data, err := os.ReadFile(f); err == nil && decl.Match(data) {
				return true
			}
		}
	}
	return false
}

// importedDirs are the folders of the packages the .ergo files in dir
// import under the name pkg (the last element of the path): a std package
// under stdDir, or a package of dir's own module, found by ergo.mod's
// module line.
func importedDirs(root, dir, pkg string) []string {
	files, _ := filepath.Glob(filepath.Join(dir, "*.ergo"))
	var out []string
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, m := range importLine().FindAllStringSubmatch(string(data), -1) {
			fields := strings.Fields(m[1])
			if len(fields) == 0 || fields[0] == "go" {
				continue
			}
			p := strings.Trim(fields[len(fields)-1], `"`)
			if path.Base(p) != pkg {
				continue
			}
			if std := filepath.Join(root, stdDir, filepath.FromSlash(p)); isDir(std) {
				out = append(out, std)
			} else if mod, name := moduleOf(dir); mod != "" && strings.HasPrefix(p, name+"/") {
				out = append(out, filepath.Join(mod, filepath.FromSlash(strings.TrimPrefix(p, name+"/"))))
			}
		}
	}
	return out
}

// moduleOf is the folder of the nearest ergo.mod at or above dir and its
// module name; "" when there is none.
func moduleOf(dir string) (string, string) {
	for d := dir; ; d = filepath.Dir(d) {
		if data, err := os.ReadFile(filepath.Join(d, "ergo.mod")); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				if name, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
					return d, strings.TrimSpace(name)
				}
			}
			return "", ""
		}
		if filepath.Dir(d) == d {
			return "", ""
		}
	}
}

func isDir(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}
