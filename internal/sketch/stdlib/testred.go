package stdlib

import (
	"fmt"
	"strings"

	"ergo.local/prototype/internal/sketch/std"
)

const TestRedPath = sourceDir + "/std-red.txt"
const NativeSkipClass = "native skip"

// TestRed holds the shipped list of standard-library obligations that
// do not yet pass. A native skip also pins its exact report and reason.
type TestRed struct{ Class, Why, ID string }

func TestReds() (map[string]TestRed, error) {
	observe("std-red.txt")
	out := map[string]TestRed{}
	for n, text := range strings.Split(std.TestRedText, "\n") {
		if strings.TrimSpace(text) == "" || strings.HasPrefix(text, "#") {
			continue
		}
		cells := strings.Split(text, "\t")
		var line TestRed
		var ok bool
		if len(cells) == 3 {
			line.ID = strings.TrimSpace(cells[2])
			line.Class, line.Why, ok = strings.Cut(cells[1], ": ")
		}
		if !ok || len(strings.Fields(cells[0])) < 2 || line.Class == "" || strings.TrimSpace(line.Why) == "" || line.ID == "" {
			return nil, fmt.Errorf("%s:%d: want \"pkg Name<TAB>CLASS: why<TAB>REGISTER-ID\", got %q", TestRedPath, n+1, text)
		}
		if _, dup := out[cells[0]]; dup {
			return nil, fmt.Errorf("%s:%d: %s is listed twice", TestRedPath, n+1, cells[0])
		}
		out[cells[0]] = line
	}
	return out, nil
}

// NativeMissingLaw is the law named by a skip or a lack-of-provider
// report, neither of which is a passing native verdict.
func NativeMissingLaw(line string) (string, bool) {
	rest, skip := strings.CutPrefix(line, "skip law ")
	if !skip {
		rest, _ = strings.CutPrefix(line, "law ")
		if rest == line || !strings.Contains(rest, " runs where ") || !strings.HasSuffix(rest, ": none is in this package or its imports") {
			return "", false
		}
	}
	name, _, _ := strings.Cut(rest, " ")
	name = strings.TrimSuffix(name, ":")
	return name, name != ""
}
