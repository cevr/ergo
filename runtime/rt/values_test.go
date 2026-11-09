package rt

import (
	"fmt"
	"strings"
	"testing"
)

// TestValuesComeDownTheAncestry: Values lists the bindings in force for a
// task, innermost first, shadowed ones included, those inherited at its
// start among them, and none its parent binds after the start.
func TestValuesComeDownTheAncestry(t *testing.T) {
	root := Root("main")
	outer := root.WithValue("k", "outer")
	root.WithValue("k", "shadow")
	var child Task
	child.init(root, "child", false, false)
	root.WithValue("late", "x")
	child.WithValue("own", "c")
	var got []string
	child.Values(func(key, value any) { got = append(got, fmt.Sprintf("%v=%v", key, value)) })
	if s := strings.Join(got, " "); s != "own=c k=shadow k=outer" {
		t.Fatalf("child values %q; want own=c k=shadow k=outer", s)
	}
	child.detach()
	outer.End()
	got = nil
	root.Values(func(key, value any) { got = append(got, fmt.Sprint(key)) })
	if len(got) != 0 {
		t.Fatalf("root values after End %q; want none", got)
	}
}
