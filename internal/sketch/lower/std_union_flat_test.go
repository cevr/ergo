package lower

import (
	"strings"
	"testing"
)

// stdUnionSource reads and builds std time's Month and Weekday: a date's
// month, a slice of months, a comparison and a match in std's MonthNumber.
const stdUnionSource = `import time

func main() needs Console {
	let c = time.Calendar(time.Unix(86400 * 40, 0))
	let ms = []time.Month{time.January, c.Month, time.December}
	Console.Println("\(time.MonthNumber(ms[1])) \(ms[0] == ms[1]) \(time.WeekdayNumber(c.Weekday))")
}
`

// TestStdUnionsAreFlat is MSB3's promised form (types.md Unions 6): a std
// union the rule admits is flat, as one of the program's is, so time's
// field-less Month and Weekday are a tag, one byte, where they were
// interfaces of a box each. The reference msb3Lowering "off" reads the
// same packages, so they are flat there too: a reference that boxed std's
// unions and not a user copy's would be a privilege (STD-SPEED-BY-SHAPE).
// Each lowering type-checks (go/types). The run is the examples' (cron's
// calendar, jsonv's decoded kinds) under both.
// Mutants: msb3-std-sourced-only, ssb-msb3-off-sourced-only,
// msb3-std-layout-unknown, msb3-fieldless-of-call.
func TestStdUnionsAreFlat(t *testing.T) {
	flat := mustLower(t, "stdflat", stdUnionSource)
	saved := msb3Lowering
	msb3Lowering = "off"
	ref := mustLower(t, "stdflatref", stdUnionSource)
	msb3Lowering = saved
	for _, name := range []string{"Month", "Weekday"} {
		if !strings.Contains(flat.mainGo(), "type "+goOf("time", name)+" struct {") {
			t.Errorf("std time's %s is not a flat struct", name)
		}
		if !strings.Contains(ref.mainGo(), "type "+goOf("time", name)+" struct {") {
			t.Errorf("under msb3Lowering off, std time's %s is not a flat struct: the reference reads std unlike a user package", name)
		}
	}
	// A field-less variant's literal is the tag literal, a constant, so
	// std time's months() stays within Go's inlining budget and its slice
	// does not escape (cron's test allocated 9M times with of calls).
	if !strings.Contains(flat.mainGo(), "("+goOf("time", "Month")+"{tag: 0})") {
		t.Errorf("std time's January is not the tag literal (Month{tag: 0})")
	}
	typecheckGo(t, []*lowered{flat, ref})
}
