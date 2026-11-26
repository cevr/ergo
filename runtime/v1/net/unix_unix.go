//go:build unix

package net

import (
	"context"
	"encoding/binary"
	"io"
	gonet "net"
	"os"
	"syscall"

	"ergo.local/prototype/runtime/rt"
)

type osUnixListener struct {
	*gonet.UnixListener
}

type osUnixConn struct {
	*gonet.UnixConn
}

func dupCloseOnExec(rawFD int) (*os.File, error) {
	if rawFD < 0 {
		return nil, NetFailed{Reason: "invalid file descriptor"}
	}
	syscall.ForkLock.RLock()
	newFD, err := syscall.Dup(rawFD)
	if err == nil {
		syscall.CloseOnExec(newFD)
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		return nil, NetFailed{Reason: err.Error()}
	}
	return os.NewFile(uintptr(newFD), "fd"), nil
}

func osCloseFile(f *os.File) {
	_ = f.Close()
}

func osFileFd(f *os.File) int {
	sc, err := f.SyscallConn()
	if err != nil {
		return -1
	}
	out := -1
	if err := sc.Control(func(u uintptr) {
		out = int(u)
	}); err != nil {
		return -1
	}
	return out
}

func osDupFile(f *os.File) (*os.File, error) {
	sc, err := f.SyscallConn()
	if err != nil {
		return nil, NetFailed{Reason: err.Error()}
	}
	var dup *os.File
	var dupErr error
	if err := sc.Control(func(u uintptr) {
		dup, dupErr = dupCloseOnExec(int(u))
	}); err != nil {
		return nil, NetFailed{Reason: err.Error()}
	}
	if dupErr != nil {
		return nil, dupErr
	}
	return dup, nil
}

func osAdoptFD(fd int) (*FD, error) {
	f, err := dupCloseOnExec(fd)
	if err != nil {
		return nil, err
	}
	return &FD{file: f}, nil
}

func osDupListenerFD(ln gonet.Listener) (*FD, error) {
	type filer interface {
		File() (*os.File, error)
	}
	fl, ok := ln.(filer)
	if !ok {
		return nil, NetFailed{Reason: "listener does not expose a file descriptor"}
	}
	f, err := fl.File()
	if err != nil {
		return nil, NetFailed{Reason: err.Error()}
	}
	return &FD{file: f}, nil
}

func osDupConnFD(c gonet.Conn) (*FD, error) {
	type filer interface {
		File() (*os.File, error)
	}
	fc, ok := c.(filer)
	if !ok {
		return nil, NetFailed{Reason: "connection does not expose a file descriptor"}
	}
	f, err := fc.File()
	if err != nil {
		return nil, NetFailed{Reason: err.Error()}
	}
	return &FD{file: f}, nil
}

func osDupUnixListenerFD(l *osUnixListener) (*FD, error) {
	f, err := l.File()
	if err != nil {
		return nil, NetFailed{Reason: err.Error()}
	}
	return &FD{file: f}, nil
}

func osDupUnixConnFD(c *osUnixConn) (*FD, error) {
	f, err := c.File()
	if err != nil {
		return nil, NetFailed{Reason: err.Error()}
	}
	return &FD{file: f}, nil
}

func osIntoListener(f *os.File) (*Listener, error) {
	defer func() { _ = f.Close() }()
	ln, err := gonet.FileListener(f)
	if err != nil {
		return nil, NetFailed{Reason: err.Error()}
	}
	if _, isUnix := ln.(*gonet.UnixListener); isUnix {
		_ = ln.Close()
		return nil, NetFailed{Reason: "FD is a unix listener, use UnixListenFromFD"}
	}
	return &Listener{host: ln}, nil
}

func osIntoUnixListener(f *os.File) (*UnixListener, error) {
	defer func() { _ = f.Close() }()
	ln, err := gonet.FileListener(f)
	if err != nil {
		return nil, NetFailed{Reason: err.Error()}
	}
	ul, ok := ln.(*gonet.UnixListener)
	if !ok {
		_ = ln.Close()
		return nil, NetFailed{Reason: "FD is not a unix listener"}
	}
	ul.SetUnlinkOnClose(false)
	return &UnixListener{host: &osUnixListener{ul}}, nil
}

func osIntoConn(f *os.File) (*Conn, error) {
	defer func() { _ = f.Close() }()
	c, err := gonet.FileConn(f)
	if err != nil {
		return nil, NetFailed{Reason: err.Error()}
	}
	if _, isUnix := c.(*gonet.UnixConn); isUnix {
		_ = c.Close()
		return nil, NetFailed{Reason: "FD is a unix connection, use UnixConnFromFD"}
	}
	return &Conn{host: c}, nil
}

func osIntoUnixConn(f *os.File) (*UnixConn, error) {
	defer func() { _ = f.Close() }()
	c, err := gonet.FileConn(f)
	if err != nil {
		return nil, NetFailed{Reason: err.Error()}
	}
	uc, ok := c.(*gonet.UnixConn)
	if !ok {
		_ = c.Close()
		return nil, NetFailed{Reason: "FD is not a unix connection"}
	}
	return &UnixConn{host: &osUnixConn{uc}}, nil
}

func osListenUnix(path string) (*UnixListener, error) {
	if path == "" {
		return nil, NetListenFailed{Addr: path, Reason: "missing address"}
	}
	ln, err := gonet.ListenUnix("unix", &gonet.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, NetListenFailed{Addr: path, Reason: reason(err)}
	}
	return &UnixListener{host: &osUnixListener{ln}}, nil
}

func osDialUnix(t *rt.Task, path string) (*UnixConn, error) {
	if path == "" {
		return nil, NetDialFailed{Addr: path, Reason: "missing address"}
	}
	ctx := context.Background()
	if t.Done() != nil {
		ctx = rt.GoContext(t)
	}
	c, err := (&gonet.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		t.Check()
		return nil, NetDialFailed{Addr: path, Reason: reason(err)}
	}
	uc, ok := c.(*gonet.UnixConn)
	if !ok {
		_ = c.Close()
		return nil, NetDialFailed{Addr: path, Reason: "unexpected connection type"}
	}
	return &UnixConn{host: &osUnixConn{uc}}, nil
}

func osUnixListenerAddr(l *osUnixListener) string {
	if a := l.Addr(); a != nil {
		return a.String()
	}
	return ""
}

func osUnixListenerSetUnlink(l *osUnixListener, unlink bool) {
	l.SetUnlinkOnClose(unlink)
}

func osUnixListenerClose(l *osUnixListener) {
	_ = l.Close()
}

func osUnixListenerAccept(t *rt.Task, l *osUnixListener) (*UnixConn, error) {
	disarm := onStop(t, l.SetDeadline)
	conn, err := l.AcceptUnix()
	fired := disarm()
	if err == nil {
		return &UnixConn{host: &osUnixConn{conn}}, nil
	}
	if fired {
		t.Check()
	}
	return nil, NetFailed{Reason: err.Error()}
}

func osUnixConnLocalAddr(c *osUnixConn) string {
	if a := c.LocalAddr(); a != nil {
		return a.String()
	}
	return ""
}

func osUnixConnRemoteAddr(c *osUnixConn) string {
	if a := c.RemoteAddr(); a != nil {
		return a.String()
	}
	return ""
}

func osUnixConnClose(c *osUnixConn) {
	_ = c.Close()
}

func osUnixConnRead(t *rt.Task, c *osUnixConn, buf []byte) (string, error) {
	disarm := onStop(t, c.SetReadDeadline)
	n, err := c.Read(buf)
	fired := disarm()
	if n > 0 {
		return string(buf[:n]), nil
	}
	if err == io.EOF {
		return "", nil
	}
	if fired {
		t.Check()
	}
	return "", NetFailed{Reason: err.Error()}
}

func osUnixConnWrite(t *rt.Task, c *osUnixConn, data string) error {
	disarm := onStop(t, c.SetWriteDeadline)
	_, err := io.WriteString(c.UnixConn, data)
	fired := disarm()
	if err == nil {
		return nil
	}
	if fired {
		t.Check()
	}
	return NetFailed{Reason: err.Error()}
}

func osUnixConnSendFDs(t *rt.Task, c *osUnixConn, data string, fds []*FD) error {
	if len(fds) > MaxControlFDs {
		return NetFailed{Reason: "too many FDs"}
	}
	if len(data) > unixFrameMax {
		return NetFailed{Reason: "FD message data too large"}
	}
	tempFiles := make([]*os.File, 0, len(fds))
	defer func() {
		for _, tf := range tempFiles {
			_ = tf.Close()
		}
	}()
	rawFDs := make([]int, len(fds))
	for i, fd := range fds {
		if fd == nil {
			return NetFailed{Reason: "use of closed FD"}
		}
		fd.mu.Lock()
		closed := fd.closed
		pipe := fd.pipe
		file := fd.file
		var tf *os.File
		var err error
		if !closed && pipe == nil && file != nil {
			tf, err = osDupFile(file)
		}
		fd.mu.Unlock()
		if closed || file == nil {
			return NetFailed{Reason: "use of closed FD"}
		}
		if pipe != nil {
			return NetFailed{Reason: "cannot send in-memory FD over host socket"}
		}
		if err != nil {
			return err
		}
		tempFiles = append(tempFiles, tf)
		raw := osFileFd(tf)
		if raw < 0 {
			return NetFailed{Reason: "use of closed FD"}
		}
		rawFDs[i] = raw
	}

	msg := make([]byte, 4+len(data))
	binary.BigEndian.PutUint32(msg[:4], uint32(len(data)))
	copy(msg[4:], data)

	var oob []byte
	if len(rawFDs) > 0 {
		oob = syscall.UnixRights(rawFDs...)
	}

	disarm := onStop(t, c.SetWriteDeadline)
	n, _, err := c.WriteMsgUnix(msg, oob, nil)
	if err == nil && n < len(msg) {
		for off := n; off < len(msg); {
			nw, werr := c.Write(msg[off:])
			off += nw
			if werr != nil {
				err = werr
				break
			}
		}
	}
	fired := disarm()
	if err == nil {
		return nil
	}
	if fired {
		t.Check()
	}
	return NetFailed{Reason: err.Error()}
}

func osUnixConnRecvFDs(t *rt.Task, c *osUnixConn, maxFDs int) (NetReceivedFDs, error) {
	if maxFDs < 0 || maxFDs > MaxControlFDs {
		return NetReceivedFDs{}, NetFailed{Reason: "invalid maxFDs"}
	}
	oob := make([]byte, syscall.CmsgSpace(max(maxFDs, 1)*4))
	var hdr [4]byte
	hdrOff := 0
	var files []*os.File
	closeFiles := func() {
		for _, f := range files {
			_ = f.Close()
		}
		files = nil
	}

	disarm := onStop(t, c.SetReadDeadline)
	for hdrOff < 4 {
		if hdrOff == 0 {
			n, oobn, flags, _, err := c.ReadMsgUnix(hdr[:], oob)
			if oobn > 0 {
				scms, perr := syscall.ParseSocketControlMessage(oob[:oobn])
				for _, scm := range scms {
					got, rerr := syscall.ParseUnixRights(&scm)
					for _, raw := range got {
						syscall.CloseOnExec(raw)
						files = append(files, os.NewFile(uintptr(raw), "fd"))
					}
					if rerr != nil && perr == nil {
						perr = rerr
					}
				}
				if flags&syscall.MSG_CTRUNC != 0 || len(files) > maxFDs {
					disarm()
					closeFiles()
					return NetReceivedFDs{}, NetFailed{Reason: "received FDs exceeded maxFDs"}
				}
				if perr != nil {
					disarm()
					closeFiles()
					return NetReceivedFDs{}, NetFailed{Reason: perr.Error()}
				}
			}
			hdrOff += n
			if err != nil {
				if err == io.EOF && hdrOff == 4 {
					break
				}
				fired := disarm()
				closeFiles()
				if fired {
					t.Check()
				}
				return NetReceivedFDs{}, NetFailed{Reason: err.Error()}
			}
			continue
		}
		n, err := c.Read(hdr[hdrOff:])
		hdrOff += n
		if err != nil {
			if err == io.EOF && hdrOff == 4 {
				break
			}
			fired := disarm()
			closeFiles()
			if fired {
				t.Check()
			}
			return NetReceivedFDs{}, NetFailed{Reason: err.Error()}
		}
	}

	dataLen := int(binary.BigEndian.Uint32(hdr[:]))
	if dataLen > unixFrameMax {
		disarm()
		closeFiles()
		return NetReceivedFDs{}, NetFailed{Reason: "FD message frame too large"}
	}

	buf := make([]byte, dataLen)
	for pos := 0; pos < dataLen; {
		n, err := c.Read(buf[pos:])
		pos += n
		if err != nil {
			if err == io.EOF && pos == dataLen {
				break
			}
			fired := disarm()
			closeFiles()
			if fired {
				t.Check()
			}
			if err == io.EOF {
				return NetReceivedFDs{}, NetFailed{Reason: io.ErrUnexpectedEOF.Error()}
			}
			return NetReceivedFDs{}, NetFailed{Reason: err.Error()}
		}
	}
	disarm()

	outFDs := make([]*FD, len(files))
	for i, f := range files {
		outFDs[i] = &FD{file: f}
	}
	return NetReceivedFDs{Data: string(buf), FDs: outFDs}, nil
}
