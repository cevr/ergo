package main

import (
	"reflect"
	"testing"
)

// warmKeyExempt are the fields of fileOptions that warmOptions leaves out,
// each with why its value cannot change a warm answer.
var warmKeyExempt = map[string]string{
	"command":    "a key part of its own (prepareWarm)",
	"file":       "a key part of its own: the admitted source (prepareWarm)",
	"output":     "where the answer is placed (placeWarm), not the answer",
	"work":       "a -work request takes no warm answer (prepareWarm)",
	"report":     "a -report request takes no warm answer (prepareWarm)",
	"goText":     "a -go-text request takes no warm answer (prepareWarm)",
	"emitted":    "--emitted prints a digest before any warm lookup (printEmitted)",
	"strict":     "only diagnostics and lint read it, and neither takes a warm answer",
	"explore":    "an --explore test takes no warm answer (testCacheable)",
	"search":     "a schedule search is --explore's (testCacheable)",
	"receipt":    "a --receipt test takes no warm answer (testCacheable)",
	"uncached":   "a --count 1 test takes no warm answer (testCacheable)",
	"fresh":      "--fresh reads no warm answer and keeps the one it runs to, which is the answer without it (startWarm)",
	"mutants":    "verified separately by verify mutants",
	"derived":    "verified separately by verify mutants --derived",
	"pruneCases": "prunes ergo.cases rather than taking a warm build/test answer",
	"sharding":   "parallelism and sharding change scheduling, never a package's answer",
	"args":       "run's program arguments: the warm answer is the executable, which they do not change",
	"affected":   "--affected selects which packages run; each selected package runs as `ergo test` runs it (affected.go)",
	"level":      "a key part of its own: the toolchain's CPU level (prepareWarm, tc.Level; BLV U3)",
	"lib":        "--lib writes a Go library module before any warm lookup",
	"release":    "a key part of its own: --release's profile bytes (prepareWarm, tc.PGO; BLV U4)",
	"profile":    "a key part of its own: the profile's bytes (prepareWarm, tc.PGO; BLV U4)",
}

// TestWarmKeyCoversOptions: every option of fileOptions changes the warm
// key (warmOptions) unless warmKeyExempt says why it cannot change the
// answer. The field list is read from the struct, so a new option is
// keyed or exempted by name.
//
// Mutant: warm-key-drops-rewrites (warmOptions leaves out opts.rewrites,
// so `ergo build -rewrites=off` replays the -rewrites=on executable; the
// FUSION receipt measured one binary twice).
func TestWarmKeyCoversOptions(t *testing.T) {
	ty := reflect.TypeFor[fileOptions]()
	base := warmOptions(fileOptions{})
	for i := range ty.NumField() {
		f := ty.Field(i)
		if _, ok := warmKeyExempt[f.Name]; ok {
			continue
		}
		var opts fileOptions
		if !setNonZero(reflect.ValueOf(&opts).Elem().Field(i)) {
			t.Errorf("fileOptions.%s: the test cannot set a %s", f.Name, f.Type)
			continue
		}
		if warmOptions(opts) == base {
			t.Errorf("fileOptions.%s does not change the warm key; key it in warmOptions or exempt it in warmKeyExempt with why", f.Name)
		}
	}
	for name := range warmKeyExempt {
		if _, ok := ty.FieldByName(name); !ok {
			t.Errorf("warmKeyExempt names %s, which fileOptions no longer has", name)
		}
	}
}

// setNonZero sets v, an unexported field reached through an addressable
// struct, to a value other than its zero; false for a kind it does not set.
func setNonZero(v reflect.Value) bool {
	v = reflect.NewAt(v.Type(), v.Addr().UnsafePointer()).Elem()
	switch v.Kind() {
	case reflect.String:
		v.SetString("x")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int:
		v.SetInt(1)
	case reflect.Slice:
		v.Set(reflect.Append(v, reflect.Zero(v.Type().Elem())))
	case reflect.Struct:
		for i := range v.NumField() {
			if setNonZero(v.Field(i)) {
				return true
			}
		}
		return false
	default:
		return false
	}
	return true
}
