package bindc

import (
	"fmt"
	"testing"
)

// TestOpenTUI093Fragments is the smallest repro of the two refusals of
// OpenTUI v0.9.3's header (BINDC-OT): BC024 at opentui.h:1458,
// `INT32_C(-1)`, and BC003 at opentui.h:2784, a #define continued with a
// backslash-newline. The fragment holds those lines verbatim; each
// constant has the value, type and canonical spelling C gives it. It
// catches bindc-intc-neg-spelling (the spelling drops the minus), which
// the macro property does not see, as it checks values and types only.
func TestOpenTUI093Fragments(t *testing.T) {
	m := parseHeader(t, "testdata/opentui", "v0.9.3-fragments.h")
	want := map[string]string{
		"OT_EMBEDDED_TERMINAL_BUTTON_NONE": "int32_t -1 INT32_C(-1)",
		"OT_KITTY_KEYBOARD_ALL":            "uint32_t 31 (OT_KITTY_KEYBOARD_DISAMBIGUATE | OT_KITTY_KEYBOARD_EVENT_TYPES | OT_KITTY_KEYBOARD_ALTERNATE_KEYS | OT_KITTY_KEYBOARD_ALL_KEYS_AS_ESCAPES | OT_KITTY_KEYBOARD_REPORT_TEXT)",
		"OT_CONTROL_CAPABILITY_RESPONSE":   "uint32_t 1 UINT32_C(1)",
	}
	for _, c := range m.Constants {
		w, ok := want[c.Name]
		if !ok {
			continue
		}
		delete(want, c.Name)
		value := fmt.Sprint(c.Uint64())
		if c.Signed() {
			value = fmt.Sprint(c.Int64())
		}
		if got := string(c.Type) + " " + value + " " + c.Expr; got != w {
			t.Errorf("%s: %s, want %s", c.Name, got, w)
		}
	}
	for name := range want {
		t.Errorf("%s: no constant", name)
	}
}
