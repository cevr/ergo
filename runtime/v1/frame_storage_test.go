package v1

import (
	"reflect"
	"testing"

	"ergo.local/prototype/runtime/rt"
)

// TestFrameStoresOneReleaseList reproduces the escaping Scope in
// process.Run. These frames already own their entries; embedding another
// owning scope makes every escaping frame pay for its unused inline list.
func TestFrameStoresOneReleaseList(t *testing.T) {
	for _, frame := range []reflect.Type{reflect.TypeFor[Scope](), reflect.TypeFor[Exits]()} {
		for field := range frame.Fields() {
			if field.Type == reflect.TypeFor[rt.Scope]() {
				t.Errorf("%s.%s embeds an unused second release list (%d bytes)", frame.Name(), field.Name, field.Type.Size())
			}
		}
	}
}
