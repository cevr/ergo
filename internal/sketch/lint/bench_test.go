package lint_test

import (
	"path/filepath"
	"sync"
	"testing"

	"ergo.local/prototype/internal/sketch/lint"
	"ergo.local/prototype/internal/sketchfront"
	sdk "ergo.local/prototype/lint"
)

var (
	benchCorpusOnce   sync.Once
	benchCorpusInputs []lint.Input
	benchCorpusErr    error
)

func loadBenchCorpus(tb testing.TB) []lint.Input {
	tb.Helper()
	benchCorpusOnce.Do(func() {
		dirs := []string{
			filepath.Join("..", "..", "..", "compiler", "syntax"),
			filepath.Join("..", "..", "..", "examples", "jobs"),
			filepath.Join("..", "..", "..", "examples", "confparse"),
			filepath.Join("..", "..", "..", "examples", "bookmarks"),
		}
		for _, dir := range dirs {
			targets, err := sketchfront.DiscoverPackages(dir)
			if err != nil {
				benchCorpusErr = err
				return
			}
			for _, target := range targets {
				a, err := sketchfront.CheckDir(target.Dir)
				if err != nil {
					benchCorpusErr = err
					return
				}
				in := lint.InputOf(a)
				if in.Checked {
					benchCorpusInputs = append(benchCorpusInputs, in)
				}
			}
		}
	})
	if benchCorpusErr != nil {
		tb.Fatal(benchCorpusErr)
	}
	if len(benchCorpusInputs) == 0 {
		tb.Fatal("bench corpus loaded 0 checked packages")
	}
	return benchCorpusInputs
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

func BenchmarkLintAllRules(b *testing.B) {
	inputs := loadBenchCorpus(b)
	conf := benchAllRulesConfig(b)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		n := 0
		for _, in := range inputs {
			n += len(conf.Run(in).Findings)
		}
		if n < 0 {
			b.Fatal(n)
		}
	}
}

func BenchmarkLintStrict(b *testing.B) {
	inputs := loadBenchCorpus(b)
	conf := benchPresetConfig(b, lint.Strict)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		n := 0
		for _, in := range inputs {
			n += len(conf.Run(in).Findings)
		}
		if n < 0 {
			b.Fatal(n)
		}
	}
}

func BenchmarkLintLayout(b *testing.B) {
	inputs := loadBenchCorpus(b)
	conf := benchPresetConfig(b, lint.Layout)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		n := 0
		for _, in := range inputs {
			n += len(conf.Run(in).Findings)
		}
		if n < 0 {
			b.Fatal(n)
		}
	}
}

func BenchmarkLintSpeedAndAdvisory(b *testing.B) {
	inputs := loadBenchCorpus(b)
	conf := benchPresetConfig(b, lint.Speed, lint.Advisory)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		n := 0
		for _, in := range inputs {
			n += len(conf.Run(in).Findings)
		}
		if n < 0 {
			b.Fatal(n)
		}
	}
}

func BenchmarkLintFacts(b *testing.B) {
	inputs := loadBenchCorpus(b)
	families := sdk.Families()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		n := 0
		for _, in := range inputs {
			n += len(lint.Facts(in, families).Callables)
		}
		if n < 0 {
			b.Fatal(n)
		}
	}
}
