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
//	cores N            the budget in cores (required)
//	memory SIZE        the budget in memory, as 40G, 512M or bytes
//	                   (default 0: memory is not budgeted)
//	default-memory SIZE  the reservation of a command never measured
//	                   (default 8G, at most memory)
//	share N            the lane class's share, in cores (default 0: none)
//	state DIR          the folder of the budget's state (default: FILE.state)
//	log FILE           a line per queue, take and end (default: none)
//
// Every decision reads the file again, so an edit takes effect at the next
// decision, and a waiter is woken by the edit (watchConfig).
type Config struct {
	Path          string
	Cores         int
	Memory        int64
	DefaultMemory int64
	Share         int
	Backfill      int
	State         string
	Log           string
}

// LoadConfig reads the budget's file.
func LoadConfig(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, err
	}
	defer f.Close()
	c := Config{Path: path, State: path + ".state", DefaultMemory: 8 << 30, Backfill: 4}
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line, _, _ := strings.Cut(sc.Text(), "#")
		key, value, _ := strings.Cut(strings.TrimSpace(line), " ")
		value = strings.TrimSpace(value)
		bad := func() error { return fmt.Errorf("%s:%d: %s wants a value, has %q", path, n, key, value) }
		switch key {
		case "":
		case "cores", "share", "backfill":
			v, err := strconv.Atoi(value)
			if err != nil || v < 0 {
				return Config{}, bad()
			}
			switch key {
			case "cores":
				c.Cores = v
			case "share":
				c.Share = v
			default:
				c.Backfill = v
			}
		case "memory", "default-memory":
			v, err := ParseSize(value)
			if err != nil {
				return Config{}, bad()
			}
			if key == "memory" {
				c.Memory = v
			} else {
				c.DefaultMemory = v
			}
		case "gate-load":
			// `gate-load 0` (off) is what every budget now does, so a file
			// the previous build read still works while the switch to this
			// one is under way; any other value would not take effect.
			if value != "0" {
				return Config{}, fmt.Errorf("%s:%d: gate-load was removed: memory admission replaces the load gate (WS4c); delete the line", path, n)
			}
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
			return Config{}, fmt.Errorf("%s:%d: unknown key %q (cores, memory, default-memory, share, backfill, state, log)", path, n, key)
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
	if c.Memory > 0 && c.DefaultMemory > c.Memory {
		c.DefaultMemory = c.Memory
	}
	return c, nil
}

// ParseSize reads a size in bytes: a whole number with an optional K, M,
// G or T suffix (powers of 1024).
func ParseSize(s string) (int64, error) {
	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "K"):
		mult = 1 << 10
	case strings.HasSuffix(s, "M"):
		mult = 1 << 20
	case strings.HasSuffix(s, "G"):
		mult = 1 << 30
	case strings.HasSuffix(s, "T"):
		mult = 1 << 40
	}
	if mult > 1 {
		s = s[:len(s)-1]
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil || v < 0 {
		return 0, fmt.Errorf("budget: %q is not a size (40G, 512M, bytes)", s)
	}
	return v * mult, nil
}
