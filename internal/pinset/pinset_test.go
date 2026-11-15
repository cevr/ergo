package pinset

import (
	"reflect"
	"testing"
)

// TestPinSetNameKeysEveryInput: a set's name changes with every field of
// its key, so a set built for another release, patch, target or flags is
// never the one a build opens. Each field in turn takes another value
// while the rest stay; every name differs from the base and from the
// others. It kills cs-pin-key-omits-patch and cs-pin-key-omits-flags (a
// set compiled by the stock compile or without -shared would be taken).
func TestPinSetNameKeysEveryInput(t *testing.T) {
	base := Key{Kind: "std", Release: "go1.27.4", Patch: "aa", GOOS: "linux", GOARCH: "amd64", Level: "v1", Flags: "-trimpath CGO_ENABLED=0 -tags="}
	names := map[string]string{base.Name(): "base"}
	v := reflect.ValueOf(&base).Elem()
	for i := range v.NumField() {
		k := base
		f := reflect.ValueOf(&k).Elem().Field(i)
		f.SetString(f.String() + "x")
		field := v.Type().Field(i).Name
		if other, seen := names[k.Name()]; seen {
			t.Errorf("a key with another %s has the name of the key with another %s: %s", field, other, k.Name())
		}
		names[k.Name()] = field
	}
}
