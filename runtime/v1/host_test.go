package v1

import (
	"os"
	"reflect"
	"testing"
)

// TestOSArgs: os.Host's Args body is the arguments after the program name,
// in a slice that does not alias os.Args.
func TestOSArgs(t *testing.T) {
	saved := os.Args
	t.Cleanup(func() { os.Args = saved })
	cases := []struct {
		argv []string
		want []string
	}{
		{[]string{"prog", "a", "b"}, []string{"a", "b"}},
		{[]string{"prog", ""}, []string{""}},
		{[]string{"prog"}, []string{}},
		{[]string{}, []string{}},
		{nil, []string{}},
	}
	for _, c := range cases {
		os.Args = c.argv
		got := OSArgs()
		if got == nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("OSArgs() with os.Args %q = %#v; want %#v", c.argv, got, c.want)
		}
	}
	os.Args = []string{"prog", "a"}
	got := OSArgs()
	got[0] = "changed"
	if os.Args[1] != "a" {
		t.Errorf("OSArgs aliases os.Args: os.Args[1] = %q after a write to the result", os.Args[1])
	}
}

// TestOSEnv: os.Host's Getenv bodies tell a variable set to the empty
// string (Some("")) from an unset one (None).
func TestOSEnv(t *testing.T) {
	const set, empty, unset = "ERGO_V1_HOST_SET", "ERGO_V1_HOST_EMPTY", "ERGO_V1_HOST_UNSET"
	t.Setenv(set, "v")
	t.Setenv(empty, "")
	t.Setenv(unset, "x")
	if err := os.Unsetenv(unset); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		key   string
		has   bool
		value string
	}{
		{set, true, "v"},
		{empty, true, ""},
		{unset, false, ""},
	}
	for _, c := range cases {
		if got := OSHasEnv(c.key); got != c.has {
			t.Errorf("OSHasEnv(%s) = %v; want %v", c.key, got, c.has)
		}
		if got := OSGetenv(c.key); got != c.value {
			t.Errorf("OSGetenv(%s) = %q; want %q", c.key, got, c.value)
		}
	}
}

// TestExitText: os.Exit's text, which report writes when it is not main's
// only reason, is its Ergo name and fields as the lowering writes a
// record's.
func TestExitText(t *testing.T) {
	got := Exit{Code: 3, Message: "[NOT_FOUND] gone"}.Error()
	if want := "os.Exit{Code:3 Message:[NOT_FOUND] gone}"; got != want {
		t.Errorf("Exit.Error() = %q; want %q", got, want)
	}
}
