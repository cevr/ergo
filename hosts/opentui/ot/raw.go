//go:build (linux || darwin) && (amd64 || arm64)

package ot

// Hand-written calls of functions the generated typed layer does not
// reach. Each keeps the generated layer's rules: ErrNotLoaded before Load,
// ErrDestroyed for a nil context, ErrTooLong for a slice its C length
// cannot hold, the Status of a failed call, runtime.Pinner before C, and
// optional cGuardIn/cGuardOut guard-page hooks under -tags ergocguard.
// No callback crosses here.
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
	vb := sliceData(b)
	var gb func()
	if cGuardIn != nil && len(b) > 0 {
		vb, gb = cGuardIn(vb, uintptr(len(b)), "ot_session_input_feed", "bytes")
	}
	vrec := sliceData(records)
	var grec func()
	if cGuardOut != nil && len(records) > 0 {
		vrec, grec = cGuardOut(vrec, uintptr(len(records))*unsafe.Sizeof(InputEvent{}), "ot_session_input_feed", "records")
	}
	vpay := sliceData(payload)
	var gpay func()
	if cGuardOut != nil && len(payload) > 0 {
		vpay, gpay = cGuardOut(vpay, uintptr(len(payload)), "ot_session_input_feed", "payload")
	}
	var pin runtime.Pinner
	pin.Pin(&session)
	pin.Pin(&d)
	if gb == nil && len(b) > 0 {
		pin.Pin(vb)
	}
	if grec == nil && len(records) > 0 {
		pin.Pin(vrec)
	}
	if gpay == nil && len(payload) > 0 {
		pin.Pin(vpay)
	}
	s := c_ot_session_input_feed(context.p, unsafe.Pointer(&session), vb, uint32(len(b)), nowNS,
		vrec, uint32(len(records)), vpay, uint32(len(payload)), unsafe.Pointer(&d))
	if gb != nil {
		gb()
	}
	if grec != nil {
		grec()
	}
	if gpay != nil {
		gpay()
	}
	pin.Unpin()
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
	vbuf := sliceData(buf)
	var gbuf func()
	if cGuardOut != nil && len(buf) > 0 {
		vbuf, gbuf = cGuardOut(vbuf, uintptr(len(buf)), "ot_session_read_output", "buf")
	}
	var pin runtime.Pinner
	pin.Pin(&session)
	pin.Pin(&t)
	if gbuf == nil && len(buf) > 0 {
		pin.Pin(vbuf)
	}
	s := c_ot_session_read_output(context.p, unsafe.Pointer(&session), vbuf, uint32(len(buf)), unsafe.Pointer(&t))
	if gbuf != nil {
		gbuf()
	}
	pin.Unpin()
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
	vb := sliceData(b)
	var gb func()
	if cGuardOut != nil && len(b) > 0 {
		vb, gb = cGuardOut(vb, uintptr(len(b)), "ot_buffer_lease_write_resolved_chars", "b")
	}
	vl := sliceData(cellLengths)
	var gl func()
	if cGuardOut != nil && len(cellLengths) > 0 {
		vl, gl = cGuardOut(vl, uintptr(len(cellLengths)), "ot_buffer_lease_write_resolved_chars", "cellLengths")
	}
	var pin runtime.Pinner
	pin.Pin(&lease)
	pin.Pin(&n)
	if gb == nil && len(b) > 0 {
		pin.Pin(vb)
	}
	if gl == nil && len(cellLengths) > 0 {
		pin.Pin(vl)
	}
	s := c_ot_buffer_lease_write_resolved_chars(context.p, unsafe.Pointer(&lease), vb, uint32(len(b)), breaks,
		vl, uint32(len(cellLengths)), unsafe.Pointer(&n))
	if gb != nil {
		gb()
	}
	if gl != nil {
		gl()
	}
	pin.Unpin()
	return n, statusError(s)
}
