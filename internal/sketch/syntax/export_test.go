package syntax

import (
	"reflect"
	"testing"
)

// The inputs and the tree comparison of the parse laws, for the external
// tests that format (roundtrip_test.go imports internal/hostsyntax, which
// imports this package).

// Source is an input of a law: a corpus file (Seed -1) or a seeded one.
type Source struct {
	Name, Src string
	Seed      int64
}

func sources(ins []agreeInput) []Source {
	out := make([]Source, len(ins))
	for i, in := range ins {
		out[i] = Source{Name: in.name, Src: in.src, Seed: in.seed}
	}
	return out
}

// CorpusSources are corpusInputs.
func CorpusSources(t *testing.T) []Source { return sources(corpusInputs(t)) }

// GeneratedSources are agreeSeeds over generatedProgram.
func GeneratedSources(t *testing.T) []Source { return sources(agreeSeeds(t, generatedProgram)) }

// Replay is replay's command for s.
func Replay(s Source, test string) string {
	return replay(agreeInput{name: s.Name, src: s.Src, seed: s.Seed}, test)
}

// SameTree is sameTree over two files.
func SameTree(want, got *File) string { return sameTree(reflect.ValueOf(want), reflect.ValueOf(got)) }
