package cases

import (
	"bytes"
	"os"
	"strconv"
	"strings"
)

// OutputCap is the most a command may write to each of its stdout and
// stderr: 1 MiB. A command that writes more is over the cap: it fails, the
// runner stops its process group at once, and the runner keeps none of the
// extra bytes. The runner fails rather than truncates, because it compares
// output byte for byte with a golden, and a golden over 1 MiB could not be
// reviewed (examples/README.md).
const OutputCap = 1 << 20

// capture is one stream of a command: the bytes read from its pipe, at most
// OutputCap of them, and whether the command wrote more. Its fields belong to
// fill until done is closed.
type capture struct {
	data []byte
	over bool
	done chan struct{}
	// mark, when set, is a text whose first appearance in the stream (or
	// the stream's end, or the cap) closes seen: ExecuteArmed's event.
	mark   []byte
	seen   chan struct{}
	closed bool // seen is closed; fill's own
	// progress, when set, gets a token (dropped when one waits) for each
	// read that brought bytes: ExecuteArmed restarts its deadline on it.
	progress chan struct{}
}

// see closes seen once the stream holds mark, or when final is true (the
// stream ended or went over the cap: no mark will come).
func (c *capture) see(final bool) {
	if c.seen == nil || c.closed {
		return
	}
	if final || bytes.Contains(c.data, c.mark) {
		close(c.seen)
		c.closed = true
	}
}

func newCapture() *capture { return &capture{done: make(chan struct{})} }

// fill copies pipe into c until EOF or a read error (execute closes the read
// end). At the first byte past the cap it marks c over, drops what it kept and
// calls overCap, which stops the command's group; then it reads on and
// discards, so no writer blocks before its group is gone. fill holds no logic
// that can panic: a panic on its goroutine would end the runner with no
// deferred call, so with no group stop.
func (c *capture) fill(pipe *os.File, overCap func()) {
	defer close(c.done)
	chunk := make([]byte, 32<<10)
	for {
		n, err := pipe.Read(chunk)
		if !c.over {
			if len(c.data)+n <= OutputCap {
				c.data = append(c.data, chunk[:n]...)
			} else {
				c.over = true
				c.data = nil
				overCap()
			}
		}
		c.see(c.over || err != nil)
		if n > 0 && c.progress != nil {
			select {
			case c.progress <- struct{}{}:
			default:
			}
		}
		if err != nil {
			return
		}
	}
}

// overCap names each stream of o over the cap, joined by "; ", or returns ""
// when both are within it.
func (o Outcome) OverCap() string {
	var over []string
	if o.StdoutOver {
		over = append(over, "stdout over "+strconv.Itoa(OutputCap)+" bytes")
	}
	if o.StderrOver {
		over = append(over, "stderr over "+strconv.Itoa(OutputCap)+" bytes")
	}
	return strings.Join(over, "; ")
}
