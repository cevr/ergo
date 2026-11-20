package budget

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config is the host owner's file of the budget: one `key value` per line,
// `#` starts a comment.
//
//	cores N       the budget B, in cores (required)
//	share N       the lane class's share, in cores (default 0: none)
//	gate-load N   the interim load gate: the lane class waits while the
//	              1-minute load is over N (default 0: off; WS4c removes it)
//	state DIR     the folder of the budget's state (default: FILE.state)
//	log FILE      a line per queue, take and end (default: none)
type Config struct {
	Cores    int
	Share    int
	GateLoad float64
	State    string
	Log      string
}

// LoadConfig reads the budget's file.
func LoadConfig(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, err
	}
	defer f.Close()
	c := Config{State: path + ".state"}
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line, _, _ := strings.Cut(sc.Text(), "#")
		key, value, _ := strings.Cut(strings.TrimSpace(line), " ")
		value = strings.TrimSpace(value)
		bad := func() error { return fmt.Errorf("%s:%d: %s wants a value, has %q", path, n, key, value) }
		switch key {
		case "":
		case "cores", "share":
			v, err := strconv.Atoi(value)
			if err != nil || v < 0 {
				return Config{}, bad()
			}
			if key == "cores" {
				c.Cores = v
			} else {
				c.Share = v
			}
		case "gate-load":
			v, err := strconv.ParseFloat(value, 64)
			if err != nil || v < 0 {
				return Config{}, bad()
			}
			c.GateLoad = v
		case "state", "log":
			if value == "" {
				return Config{}, bad()
			}
			if !filepath.IsAbs(value) {
				value = filepath.Join(filepath.Dir(path), value)
			}
			if key == "state" {
				c.State = value
			} else {
				c.Log = value
			}
		default:
			return Config{}, fmt.Errorf("%s:%d: unknown key %q (cores, share, gate-load, state, log)", path, n, key)
		}
	}
	if err := sc.Err(); err != nil {
		return Config{}, err
	}
	if c.Cores < 1 {
		return Config{}, fmt.Errorf("%s: no cores line: the budget is the host owner's number", path)
	}
	if c.Share > c.Cores {
		return Config{}, fmt.Errorf("%s: share %d is over cores %d", path, c.Share, c.Cores)
	}
	return c, nil
}
