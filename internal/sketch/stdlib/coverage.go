package stdlib

import "sync"

// The coverage table: which Go standard-library packages a std package
// covers, so a program that imports the Go package (import go P) can use
// std instead (lint rule foreign-where-std; plan LINT1). This is the one
// place std declares it. TestStdCoverageIsTotal keeps it total: every std
// package is in coverage or in coversNone, and every Go path has a
// binding.

// coverage maps a std package to the Go packages it covers.
var coverage = sync.OnceValue(func() map[string][]string {
	return map[string][]string{
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
		"hmac":     {"crypto/hmac"},
		"html":     {"html", "html/template"},
		"http":     {"net/http"},
		"json":     {"encoding/json"},
		"log":      {"log", "log/slog"},
		"maps":     {"maps"},
		"net":      {"net"},
		"math":     {"math"},
		"os":       {"os"},
		"rand":     {"math/rand", "math/rand/v2"},
		"sha1":     {"crypto/sha1"},
		"sha256":   {"crypto/sha256"},
		"sha512":   {"crypto/sha512"},
		"sql":      {"database/sql"},
		"xml":      {"encoding/xml"},
		"slices":   {"slices", "sort"},
		"strconv":  {"strconv"},
		"strings":  {"strings"},
		"sync":     {"sync"},
		"testing":  {"testing"},
		"time":     {"time"},
		"url":      {"net/url"},
		"utf8":     {"unicode/utf8"},
	}
})

// coversNone are the std packages that replace no Go standard-library
// package: Ergo constructs (machines, actors, rows, schedules), or a Go
// counterpart outside the standard library (errgroup, x/term, x/mod's
// semver).
var coversNone = []string{
	"actor", "ansi", "api", "arena", "bench", "build", "cache", "civil", "code", "codec", "codegraph", "config", "cron", "decimal", "dense", "explore", "graph", "io", "jobs", "jobs/file", "jobs/memory", "journal", "journal/file",
	"journal/memory", "jsonschema", "kv", "kv/file", "kv/memory", "lint", "llm", "llm/memory", "llm/recorded", "machines", "mcp", "mcp/memory", "mcp/stdio", "nettest", "pace", "prompt", "proof", "proof/file", "proof/memory", "pubsub", "queue", "ratelimit", "ratelimit/memory", "rpc", "schedule", "scope", "semver", "session", "session/file", "session/memory", "set", "sqltest", "stream",
	"task", "tasks", "term", "text", "timeout", "toml", "totp", "trace", "tz", "tz/host", "tz/iana", "ui", "ui/layout", "uuid",
}

// covered maps a Go path to the std package that covers it. It is built on
// first use, so no process pays for it at start (TestStartBuildsNoTables).
var covered = sync.OnceValue(func() map[string]string {
	out := map[string]string{}
	for p, gos := range coverage() {
		for _, g := range gos {
			out[g] = p
		}
	}
	return out
})

// CoveredBy is the std package that covers the Go standard-library package
// at goPath, and whether one does.
func CoveredBy(goPath string) (string, bool) {
	p, ok := covered()[goPath]
	return p, ok
}

// Coverage is the table: each std package with the Go paths it covers,
// and the std packages that cover none.
func Coverage() (map[string][]string, []string) {
	out := map[string][]string{}
	for p, gos := range coverage() {
		out[p] = append([]string(nil), gos...)
	}
	return out, append([]string(nil), coversNone...)
}
