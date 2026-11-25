package lint_test

import (
	"path/filepath"
	"sync"
	"testing"

	"ergo.local/prototype/internal/sketch/lint"
	"ergo.local/prototype/internal/sketchfront"
	sdk "ergo.local/prototype/lint"
)

// The lint benchmarks run the rules over checked packages loaded once: the
// corpus (compiler/syntax and three examples, 13.3 KLOC) and one file of
// 105 lines (compiler/source), so a change that speeds the corpus and slows
// a small input shows both.

var (
	benchCorpus = loader{dirs: []string{"compiler/syntax", "examples/jobs", "examples/confparse", "examples/bookmarks"}}
	benchOne    = loader{dirs: []string{"compiler/source"}}
)

type loader struct {
	dirs   []string
	once   sync.Once
	inputs []lint.Input
	err    error
}

func (l *loader) load(tb testing.TB) []lint.Input {
	tb.Helper()
	l.once.Do(func() {
		for _, dir := range l.dirs {
			targets, err := sketchfront.DiscoverPackages(filepath.Join("..", "..", "..", filepath.FromSlash(dir)))
			if err != nil {
				l.err = err
				return
			}
			for _, target := range targets {
				a, err := sketchfront.CheckDir(target.Dir)
				if err != nil {
					l.err = err
					return
				}
				if in := lint.InputOf(a); in.Checked {
					l.inputs = append(l.inputs, in)
				}
			}
		}
	})
	if l.err != nil {
		tb.Fatal(l.err)
	}
	if len(l.inputs) == 0 {
		tb.Fatalf("%v: no checked package", l.dirs)
	}
	return l.inputs
}

func benchAllRulesConfig(tb testing.TB) *lint.Configuration {
	tb.Helper()
	rules := map[string]lint.Setting{
		"no-comments": {Severity: lint.Warning, HasSeverity: true},
		"file-cycle":  {Severity: lint.Warning, HasSeverity: true},
	}
	conf, err := lint.Builtin().Configure(
		lint.Config{Version: lint.ConfigVersion, Rules: rules},
		[]string{lint.Justifiers, lint.Strict, lint.Layout, lint.Advisory, lint.Speed},
	)
	if err != nil {
		tb.Fatal(err)
	}
	return conf
}

func benchPresetConfig(tb testing.TB, presets ...string) *lint.Configuration {
	tb.Helper()
	conf, err := lint.Builtin().Configure(lint.Config{Version: lint.ConfigVersion}, presets)
	if err != nil {
		tb.Fatal(err)
	}
	return conf
}

func benchRun(b *testing.B, l *loader, conf func(testing.TB) *lint.Configuration) {
	inputs := l.load(b)
	c := conf(b)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		for _, in := range inputs {
			c.Run(in)
		}
	}
}

func BenchmarkLintAllRules(b *testing.B) { benchRun(b, &benchCorpus, benchAllRulesConfig) }

func BenchmarkLintOneFile(b *testing.B) { benchRun(b, &benchOne, benchAllRulesConfig) }

func BenchmarkLintStrict(b *testing.B) {
	benchRun(b, &benchCorpus, func(tb testing.TB) *lint.Configuration { return benchPresetConfig(tb, lint.Strict) })
}

func BenchmarkLintLayout(b *testing.B) {
	benchRun(b, &benchCorpus, func(tb testing.TB) *lint.Configuration { return benchPresetConfig(tb, lint.Layout) })
}

func BenchmarkLintSpeedAndAdvisory(b *testing.B) {
	benchRun(b, &benchCorpus, func(tb testing.TB) *lint.Configuration {
		return benchPresetConfig(tb, lint.Speed, lint.Advisory)
	})
}

func BenchmarkLintFacts(b *testing.B) {
	inputs := benchCorpus.load(b)
	families := sdk.Families()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		for _, in := range inputs {
			lint.Facts(in, families)
		}
	}
}
