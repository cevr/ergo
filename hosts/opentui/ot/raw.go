//go:build (linux || darwin) && (amd64 || arm64)

package ot

// Hand-written calls of functions the generated typed layer does not
// reach. Each keeps the generated layer's rules: ErrNotLoaded before Load,
// ErrDestroyed for a nil context, ErrTooLong for a slice its C length
// cannot hold, the Status of a failed call, and Go memory kept alive
// until C returns. No callback crosses here.
//
// DELETE each when bindc types it (a gap for lane SPL): SessionInputFeed,
// SessionReadOutput and LeaseResolvedChars are raw-only in the binding
// report ("no rule covers" a records array with a separate capacity, a
// byte buffer with a capacity, an out ticket).

import (
	"runtime"
	"unsafe"
)

func checkLen(n int) error {
	if uint64(n) > 4294967295 {
		return ErrTooLong
	}
	return nil
}

func begin(context *Context) error {
	if err := ready(); err != nil {
		return err
	}
	if context == nil || context.p == 0 {
		return ErrDestroyed
	}
	return nil
}

// SessionInputFeed parses terminal bytes into records; each record's text
// is payload[TextOffset:TextOffset+TextLen]. The drain says how many bytes
// were consumed and how many records were written.
func SessionInputFeed(context *Context, session Handle, b []byte, nowNS uint64, records []InputEvent, payload []byte) (InputDrain, error) {
	var d InputDrain
	if err := begin(context); err != nil {
		return d, err
	}
	for _, n := range []int{len(b), len(records), len(payload)} {
		if err := checkLen(n); err != nil {
			return d, err
		}
	}
	d.fill()
	s := c_ot_session_input_feed(context.p, unsafe.Pointer(&session), sliceData(b), uint32(len(b)), nowNS,
		sliceData(records), uint32(len(records)), sliceData(payload), uint32(len(payload)), unsafe.Pointer(&d))
	runtime.KeepAlive(&session)
	runtime.KeepAlive(b)
	runtime.KeepAlive(records)
	runtime.KeepAlive(payload)
	return d, statusError(s)
}

// SessionReadOutput copies the next queued output into buf; the ticket's
// ByteCount says how many bytes, and SessionCompleteOutput must take the
// ticket before the next read.
func SessionReadOutput(context *Context, session Handle, buf []byte) (OutputTicket, error) {
	var t OutputTicket
	if err := begin(context); err != nil {
		return t, err
	}
	if err := checkLen(len(buf)); err != nil {
		return t, err
	}
	s := c_ot_session_read_output(context.p, unsafe.Pointer(&session), sliceData(buf), uint32(len(buf)), unsafe.Pointer(&t))
	runtime.KeepAlive(&session)
	runtime.KeepAlive(buf)
	return t, statusError(s)
}

// LeaseResolvedChars writes the lease's cells as UTF-8 into b (pooled
// graphemes resolved), the byte length of each cell into cellLengths, and
// returns the bytes written; addLineBreaks puts a newline after each row.
func LeaseResolvedChars(context *Context, lease Handle, b []byte, addLineBreaks bool, cellLengths []byte) (uint32, error) {
	if err := begin(context); err != nil {
		return 0, err
	}
	if err := checkLen(len(b)); err != nil {
		return 0, err
	}
	if err := checkLen(len(cellLengths)); err != nil {
		return 0, err
	}
	var breaks, n uint32
	if addLineBreaks {
		breaks = 1
	}
	s := c_ot_buffer_lease_write_resolved_chars(context.p, unsafe.Pointer(&lease), sliceData(b), uint32(len(b)), breaks,
		sliceData(cellLengths), uint32(len(cellLengths)), unsafe.Pointer(&n))
	runtime.KeepAlive(&lease)
	runtime.KeepAlive(b)
	runtime.KeepAlive(cellLengths)
	return n, statusError(s)
}
