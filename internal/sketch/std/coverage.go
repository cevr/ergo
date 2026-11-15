package std

// The coverage table: which Go standard-library packages a std package
// covers, so a program that imports the Go package (import go P) can use
// std instead (lint rule foreign-where-std; plan LINT1). This is the one
// place std declares it. TestStdCoverageIsTotal keeps it total: every std
// package is in coverage or in coversNone, and every Go path has a
// binding.

// coverage maps a std package to the Go packages it covers.
var coverage = map[string][]string{
	"atomic":   {"sync/atomic"},
	"base64":   {"encoding/base64"},
	"bits":     {"math/bits"},
	"cmp":      {"cmp"},
	"crc32":    {"hash/crc32"},
	"csv":      {"encoding/csv"},
	"embed":    {"embed"},
	"exec":     {"os/exec"},
	"filepath": {"path/filepath"},
	"flag":     {"flag"},
	"fs":       {"io/fs"},
	"fstest":   {"testing/fstest"},
	"hex":      {"encoding/hex"},
	"http":     {"net/http"},
	"json":     {"encoding/json"},
	"log":      {"log", "log/slog"},
	"maps":     {"maps"},
	"math":     {"math"},
	"os":       {"os"},
	"rand":     {"math/rand", "math/rand/v2"},
	"sha256":   {"crypto/sha256"},
	"slices":   {"slices", "sort"},
	"strconv":  {"strconv"},
	"strings":  {"strings"},
	"sync":     {"sync"},
	"testing":  {"testing"},
	"time":     {"time"},
	"utf8":     {"unicode/utf8"},
}

// coversNone are the std packages that replace no Go standard-library
// package: Ergo constructs (machines, actors, rows, schedules), or a Go
// counterpart outside the standard library (errgroup, x/term).
var coversNone = []string{
	"actor", "arena", "codec", "config", "explore", "io", "journal", "journal/file",
	"journal/memory", "machines", "prompt", "queue", "rpc", "schedule", "scope",
	"task", "tasks", "term", "timeout", "toml", "trace",
}

// covered maps a Go path to the std package that covers it.
var covered = func() map[string]string {
	out := map[string]string{}
	for p, gos := range coverage {
		for _, g := range gos {
			out[g] = p
		}
	}
	return out
}()

// CoveredBy is the std package that covers the Go standard-library package
// at goPath, and whether one does.
func CoveredBy(goPath string) (string, bool) {
	p, ok := covered[goPath]
	return p, ok
}

// Coverage is the table: each std package with the Go paths it covers,
// and the std packages that cover none.
func Coverage() (map[string][]string, []string) {
	out := map[string][]string{}
	for p, gos := range coverage {
		out[p] = append([]string(nil), gos...)
	}
	return out, append([]string(nil), coversNone...)
}
