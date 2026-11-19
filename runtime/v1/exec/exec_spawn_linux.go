//go:build linux

package exec

import (
	"os"
	"strconv"
	"strings"
)

// execProcStat is the fields of /proc/<pid>/stat after the command name
// (which may hold spaces and parentheses, so the fields start after its
// last ')'): f[0] is the state, f[19] the start time. ok is false when
// the process has no entry.
func execProcStat(pid int) (f []string, ok bool) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return nil, false
	}
	s := string(data)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return nil, false
	}
	f = strings.Fields(s[i+1:])
	return f, len(f) > 19
}

// execStartTime is the start time of the process pid in clock ticks after
// boot (proc(5), field 22), or 0 when the host gives none.
func execStartTime(pid int) int64 {
	f, ok := execProcStat(pid)
	if !ok {
		return 0
	}
	n, err := strconv.ParseInt(f[19], 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// execRunning reports that the process pid exists, has not ended (a
// zombie, state Z, or a dead one, X, has) and started at start.
func execRunning(pid int, start int64) bool {
	f, ok := execProcStat(pid)
	if !ok || f[0] == "Z" || f[0] == "X" {
		return false
	}
	n, err := strconv.ParseInt(f[19], 10, 64)
	return err == nil && n == start
}
