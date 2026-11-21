/* The two lines of OpenTUI v0.9.3's opentui.h (packages/native/include,
 * commit 3dc895b) that bindc refused before BINDC-OT, with the macros they
 * name: opentui.h:1458 (INT32_C of a negated literal, BC024) and
 * opentui.h:2784 (a #define continued with a backslash-newline, BC003).
 * OpenTUI is MIT licensed; see LICENSE in this folder. */
#include <stdint.h>

#define OT_EMBEDDED_TERMINAL_BUTTON_NONE INT32_C(-1)
#define OT_EMBEDDED_TERMINAL_BUTTON_UNKNOWN INT32_C(0)

/* Kitty keyboard protocol progressive-enhancement flags. */
#define OT_KITTY_KEYBOARD_DISAMBIGUATE UINT32_C(1)
#define OT_KITTY_KEYBOARD_EVENT_TYPES UINT32_C(2)
#define OT_KITTY_KEYBOARD_ALTERNATE_KEYS UINT32_C(4)
#define OT_KITTY_KEYBOARD_ALL_KEYS_AS_ESCAPES UINT32_C(8)
#define OT_KITTY_KEYBOARD_REPORT_TEXT UINT32_C(16)
#define OT_KITTY_KEYBOARD_ALL (OT_KITTY_KEYBOARD_DISAMBIGUATE | OT_KITTY_KEYBOARD_EVENT_TYPES | \
    OT_KITTY_KEYBOARD_ALTERNATE_KEYS | OT_KITTY_KEYBOARD_ALL_KEYS_AS_ESCAPES | OT_KITTY_KEYBOARD_REPORT_TEXT)

#define OT_CONTROL_CAPABILITY_RESPONSE UINT32_C(1)
