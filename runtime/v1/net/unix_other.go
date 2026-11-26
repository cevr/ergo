//go:build !unix

package net

import (
	gonet "net"
	"os"

	"ergo.local/prototype/runtime/rt"
)

type osUnixListener struct{}

type osUnixConn struct{}

var errUnixUnsupported = NetUnsupported{Reason: "unix domain sockets and FD passing are not supported on this OS"}

func osCloseFile(f *os.File) {
	if f != nil {
		_ = f.Close()
	}
}

func osFileFd(f *os.File) int {
	if f == nil {
		return -1
	}
	return int(f.Fd())
}

func osDupFile(*os.File) (*os.File, error) {
	return nil, errUnixUnsupported
}

func osAdoptFD(int) (*FD, error) {
	return nil, errUnixUnsupported
}

func osDupListenerFD(gonet.Listener) (*FD, error) {
	return nil, errUnixUnsupported
}

func osDupConnFD(gonet.Conn) (*FD, error) {
	return nil, errUnixUnsupported
}

func osDupUnixListenerFD(*osUnixListener) (*FD, error) {
	return nil, errUnixUnsupported
}

func osDupUnixConnFD(*osUnixConn) (*FD, error) {
	return nil, errUnixUnsupported
}

func osIntoListener(f *os.File) (*Listener, error) {
	osCloseFile(f)
	return nil, errUnixUnsupported
}

func osIntoUnixListener(f *os.File) (*UnixListener, error) {
	osCloseFile(f)
	return nil, errUnixUnsupported
}

func osIntoConn(f *os.File) (*Conn, error) {
	osCloseFile(f)
	return nil, errUnixUnsupported
}

func osIntoUnixConn(f *os.File) (*UnixConn, error) {
	osCloseFile(f)
	return nil, errUnixUnsupported
}

func osListenUnix(string) (*UnixListener, error) {
	return nil, errUnixUnsupported
}

func osDialUnix(*rt.Task, string) (*UnixConn, error) {
	return nil, errUnixUnsupported
}

func osUnixListenerAddr(*osUnixListener) string     { return "" }
func osUnixListenerSetUnlink(*osUnixListener, bool) {}
func osUnixListenerClose(*osUnixListener)           {}
func osUnixListenerAccept(*rt.Task, *osUnixListener) (*UnixConn, error) {
	return nil, errUnixUnsupported
}

func osUnixConnLocalAddr(*osUnixConn) string  { return "" }
func osUnixConnRemoteAddr(*osUnixConn) string { return "" }
func osUnixConnClose(*osUnixConn)             {}

func osUnixConnRead(*rt.Task, *osUnixConn, []byte) (string, error) {
	return "", errUnixUnsupported
}

func osUnixConnWrite(*rt.Task, *osUnixConn, string) error {
	return errUnixUnsupported
}

func osUnixConnSendFDs(*rt.Task, *osUnixConn, string, []*FD) error {
	return errUnixUnsupported
}

func osUnixConnRecvFDs(*rt.Task, *osUnixConn, int) (NetReceivedFDs, error) {
	return NetReceivedFDs{}, errUnixUnsupported
}
