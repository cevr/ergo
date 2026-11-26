package lower

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/lawledger"
)

// netLawsProbe restates each law of std net's Network as a test of the
// same name (TestStdLawsRun's naming) against nettest.Pipe, the memory
// provider; runtime/v1/net TestNetHostAgrees holds the host's TCP equal
// to the pipe.
const netLawsProbe = `import net
import nettest
import slices

func sends(from net.Conn, to net.Conn, data string) bool fails net.Failed {
	try from.Write(data)
	let mut got = ""
	for len(got) < len(data) {
		let part = try to.Read()
		if part == "" {
			return false
		}
		got = got + part
	}
	got == data
}

func unixSends(from net.UnixConn, to net.UnixConn, data string) bool fails net.Failed | net.Unsupported {
	try from.Write(data)
	let mut got = ""
	for len(got) < len(data) {
		let part = try to.Read()
		if part == "" {
			return false
		}
		got = got + part
	}
	got == data
}

test NetNetworkWriteThenReadRoundTrips(data string, reply string) {
	let p = nettest.NewPipe()
	let l = try p.Listen("127.0.0.1:0")
	let c = try p.Dial(l.Addr())
	let s = try l.Accept()
	assert try sends(c, s, data)
	assert try sends(s, c, reply)
}

test NetNetworkCloseEndsReads(data string) {
	let p = nettest.NewPipe()
	let l = try p.Listen("127.0.0.1:0")
	let c = try p.Dial(l.Addr())
	let s = try l.Accept()
	try c.Write(data)
	c.Close()
	assert try net.ReadAll(s) == data
}

test NetNetworkDialAfterCloseFails() {
	let p = nettest.NewPipe()
	let l = try p.Listen("127.0.0.1:0")
	let addr = l.Addr()
	l.Close()
	let c = p.Dial(addr) catch {
	case net.DialFailed:
		return
	}
	c.Close()
	assert false
}

test NetNetworkAcceptsInDialOrder(a string, b string) {
	let p = nettest.NewPipe()
	let l = try p.Listen("127.0.0.1:0")
	let first = try p.Dial(l.Addr())
	let second = try p.Dial(l.Addr())
	let x = try l.Accept()
	let y = try l.Accept()
	assert try sends(first, x, a + "1")
	assert try sends(second, y, b + "2")
}

test NetNetworkListenTakenFails() {
	let p = nettest.NewPipe()
	let l = try p.Listen("127.0.0.1:0")
	let again = p.Listen(l.Addr()) catch {
	case net.ListenFailed:
		return
	}
	again.Close()
	assert false
}

test NetUnixNetworkWriteThenReadUnixRoundTrips(data string, reply string) {
	let p = nettest.NewPipe()
	let l = try p.ListenUnix("u1.sock")
	let c = try p.DialUnix(l.Addr())
	let s = try l.Accept()
	assert try unixSends(c, s, data)
	assert try unixSends(s, c, reply)
}

test NetUnixNetworkUnixAcceptsInDialOrder(a string, b string) {
	let p = nettest.NewPipe()
	let l = try p.ListenUnix("u2.sock")
	let first = try p.DialUnix(l.Addr())
	let second = try p.DialUnix(l.Addr())
	let x = try l.Accept()
	let y = try l.Accept()
	assert try unixSends(first, x, a + "1")
	assert try unixSends(second, y, b + "2")
}

test NetUnixNetworkSendAndRecvFDsRoundTrips(meta1 string, meta2 string, payload string) {
	let p = nettest.NewPipe()
	let ch = try p.ListenUnix("u3a.sock")
	let tx = try p.DialUnix(ch.Addr())
	let rx = try ch.Accept()
	let target = try p.ListenUnix("u3b.sock")
	target.SetUnlinkOnClose(false)
	let tfd = try target.DupFD()
	try tx.SendFDs(meta1, []net.FD{tfd})
	try tx.SendFDs(meta2, []net.FD{})
	target.Close()

	let r1 = try rx.RecvFDs(4)
	let r2 = try rx.RecvFDs(4)
	let ok2 = match r2 {
	case net.ReceivedFDs{Data: d2, FDs: fds2}:
		d2 == meta2 && len(fds2) == 0
	}
	assert ok2
	let nl = match r1 {
	case net.ReceivedFDs{Data: d1, FDs: fds1}:
		assert d1 == meta1 && len(fds1) == 1
		let (recvFD, rest1) = slices.Take(fds1, 0)
		assert len(rest1) == 0
		try net.UnixListenFromFD(recvFD)
	}
	nl.SetUnlinkOnClose(true)
	let c2 = try p.DialUnix("u3b.sock")
	let s2 = try nl.Accept()
	assert try unixSends(c2, s2, payload + "!")
}

test NetUnixNetworkSendAndRecvUnixConnFD(payload string) {
	let p = nettest.NewPipe()
	let ch = try p.ListenUnix("u4a.sock")
	let tx = try p.DialUnix(ch.Addr())
	let rx = try ch.Accept()
	let svc = try p.ListenUnix("u4b.sock")
	let peer = try p.DialUnix(svc.Addr())
	let orig = try svc.Accept()
	let cfd = try orig.DupFD()
	try tx.SendFDs("conn", []net.FD{cfd})
	orig.Close()

	let r = try rx.RecvFDs(4)
	let moved = match r {
	case net.ReceivedFDs{Data: tag, FDs: fds}:
		assert tag == "conn" && len(fds) == 1
		let (recvFD, rest) = slices.Take(fds, 0)
		assert len(rest) == 0
		try net.UnixConnFromFD(recvFD)
	}
	assert try unixSends(peer, moved, payload + "x")
	assert try unixSends(moved, peer, payload + "y")
}

test NetUnixNetworkRecvFDsRespectsBound() {
	let p = nettest.NewPipe()
	let ch = try p.ListenUnix("u5.sock")
	let tx = try p.DialUnix(ch.Addr())
	let rx = try ch.Accept()
	let rNeg = rx.RecvFDs(-1) catch {
	case net.Failed:
		let fd1 = try ch.DupFD()
		let fd2 = try ch.DupFD()
		try tx.SendFDs("two", []net.FD{fd1, fd2})
		let rOver = rx.RecvFDs(1) catch {
		case net.Failed:
			return
		case net.Unsupported:
			assert false
			return
		}
		assert rOver.Data == "" && false
		return
	case net.Unsupported:
		assert false
		return
	}
	assert rNeg.Data == "" && false
}

test NetUnixNetworkClosedUnixFails() {
	let p = nettest.NewPipe()
	let l = try p.ListenUnix("u6.sock")
	let addr = l.Addr()
	let tx = try p.DialUnix(addr)
	let rx = try l.Accept()
	l.Close()
	let da = p.DialUnix(addr) catch {
	case net.DialFailed:
		tx.Close()
		assert try rx.Read() == ""
		let rEOF = rx.RecvFDs(1) catch {
		case net.Failed:
			return
		case net.Unsupported:
			assert false
			return
		}
		assert rEOF.Data == "" && false
		return
	}
	da.Close()
	assert false
}
`

// netLawStatements is the statement of each law the probe restates, by
// ledger key; lawledger.Run holds it equal to the ledger.
var netLawStatements = map[string]string{
	"net Network.WriteThenReadRoundTrips":         `WriteThenReadRoundTrips(data string, reply string) { let l = Listen("127.0.0.1:0") catch { case ListenFailed, Denied: return false } let c = Dial(l.Addr()) catch { case DialFailed, Denied: return false } let s = l.Accept() catch { case Failed: return false } sends(c, s, data) && sends(s, c, reply) }`,
	"net Network.CloseEndsReads":                  `CloseEndsReads(data string) { let l = Listen("127.0.0.1:0") catch { case ListenFailed, Denied: return false } let c = Dial(l.Addr()) catch { case DialFailed, Denied: return false } let s = l.Accept() catch { case Failed: return false } c.Write(data) catch { case Failed: return false } c.Close() let got = ReadAll(s) catch { case Failed: return false } got == data }`,
	"net Network.DialAfterCloseFails":             `DialAfterCloseFails() { let l = Listen("127.0.0.1:0") catch { case ListenFailed, Denied: return false } let addr = l.Addr() l.Close() let c = Dial(addr) catch { case DialFailed: return true case Denied: return false } c.Close() false }`,
	"net Network.AcceptsInDialOrder":              `AcceptsInDialOrder(a string, b string) { let l = Listen("127.0.0.1:0") catch { case ListenFailed, Denied: return false } let first = Dial(l.Addr()) catch { case DialFailed, Denied: return false } let second = Dial(l.Addr()) catch { case DialFailed, Denied: return false } let x = l.Accept() catch { case Failed: return false } let y = l.Accept() catch { case Failed: return false } sends(first, x, a + "1") && sends(second, y, b + "2") }`,
	"net Network.ListenTakenFails":                `ListenTakenFails() { let l = Listen("127.0.0.1:0") catch { case ListenFailed, Denied: return false } let again = Listen(l.Addr()) catch { case ListenFailed: return true case Denied: return false } again.Close() false }`,
	"net UnixNetwork.WriteThenReadUnixRoundTrips": `WriteThenReadUnixRoundTrips(data string, reply string) { let l = ListenUnix("u1.sock") catch { case ListenFailed, Denied, Unsupported: return false } let c = DialUnix(l.Addr()) catch { case DialFailed, Denied, Unsupported: return false } let s = l.Accept() catch { case Failed, Unsupported: return false } unixSends(c, s, data) && unixSends(s, c, reply) }`,
	"net UnixNetwork.UnixAcceptsInDialOrder":      `UnixAcceptsInDialOrder(a string, b string) { let l = ListenUnix("u2.sock") catch { case ListenFailed, Denied, Unsupported: return false } let first = DialUnix(l.Addr()) catch { case DialFailed, Denied, Unsupported: return false } let second = DialUnix(l.Addr()) catch { case DialFailed, Denied, Unsupported: return false } let x = l.Accept() catch { case Failed, Unsupported: return false } let y = l.Accept() catch { case Failed, Unsupported: return false } unixSends(first, x, a + "1") && unixSends(second, y, b + "2") }`,
	"net UnixNetwork.SendAndRecvFDsRoundTrips":    `SendAndRecvFDsRoundTrips(meta1 string, meta2 string, payload string) { let ch = ListenUnix("u3a.sock") catch { case ListenFailed, Denied, Unsupported: return false } let tx = DialUnix(ch.Addr()) catch { case DialFailed, Denied, Unsupported: return false } let rx = ch.Accept() catch { case Failed, Unsupported: return false } let target = ListenUnix("u3b.sock") catch { case ListenFailed, Denied, Unsupported: return false } target.SetUnlinkOnClose(false) let tfd = target.DupFD() catch { case Failed, Unsupported: return false } tx.SendFDs(meta1, []FD{tfd}) catch { case Failed, Unsupported: return false } tx.SendFDs(meta2, []FD{}) catch { case Failed, Unsupported: return false } target.Close() let r1 = rx.RecvFDs(4) catch { case Failed, Unsupported: return false } let r2 = rx.RecvFDs(4) catch { case Failed, Unsupported: return false } let ok2 = match r2 { case ReceivedFDs{Data: d2, FDs: fds2}: d2 == meta2 && fds2.Len() == 0 } if !ok2 { return false } let nl = match r1 { case ReceivedFDs{Data: d1, FDs: fds1}: if d1 != meta1 || fds1.Len() != 1 { return false } let (recvFD, rest1) = slices.Take(fds1, 0) if rest1.Len() != 0 { return false } UnixListenFromFD(recvFD) catch { case Failed, Unsupported: return false } } nl.SetUnlinkOnClose(true) let c2 = DialUnix("u3b.sock") catch { case DialFailed, Denied, Unsupported: return false } let s2 = nl.Accept() catch { case Failed, Unsupported: return false } unixSends(c2, s2, payload + "!") }`,
	"net UnixNetwork.SendAndRecvUnixConnFD":       `SendAndRecvUnixConnFD(payload string) { let ch = ListenUnix("u4a.sock") catch { case ListenFailed, Denied, Unsupported: return false } let tx = DialUnix(ch.Addr()) catch { case DialFailed, Denied, Unsupported: return false } let rx = ch.Accept() catch { case Failed, Unsupported: return false } let svc = ListenUnix("u4b.sock") catch { case ListenFailed, Denied, Unsupported: return false } let peer = DialUnix(svc.Addr()) catch { case DialFailed, Denied, Unsupported: return false } let orig = svc.Accept() catch { case Failed, Unsupported: return false } let cfd = orig.DupFD() catch { case Failed, Unsupported: return false } tx.SendFDs("conn", []FD{cfd}) catch { case Failed, Unsupported: return false } orig.Close() let r = rx.RecvFDs(4) catch { case Failed, Unsupported: return false } let moved = match r { case ReceivedFDs{Data: tag, FDs: fds}: if tag != "conn" || fds.Len() != 1 { return false } let (recvFD, rest) = slices.Take(fds, 0) if rest.Len() != 0 { return false } UnixConnFromFD(recvFD) catch { case Failed, Unsupported: return false } } unixSends(peer, moved, payload + "x") && unixSends(moved, peer, payload + "y") }`,
	"net UnixNetwork.RecvFDsRespectsBound":        `RecvFDsRespectsBound() { let ch = ListenUnix("u5.sock") catch { case ListenFailed, Denied, Unsupported: return false } let tx = DialUnix(ch.Addr()) catch { case DialFailed, Denied, Unsupported: return false } let rx = ch.Accept() catch { case Failed, Unsupported: return false } let rNeg = rx.RecvFDs(-1) catch { case Failed: let fd1 = ch.DupFD() catch { case Failed, Unsupported: return false } let fd2 = ch.DupFD() catch { case Failed, Unsupported: return false } tx.SendFDs("two", []FD{fd1, fd2}) catch { case Failed, Unsupported: return false } let rOver = rx.RecvFDs(1) catch { case Failed: return true case Unsupported: return false } return rOver.Data == "" && false case Unsupported: return false } rNeg.Data == "" && false }`,
	"net UnixNetwork.ClosedUnixFails":             `ClosedUnixFails() { let l = ListenUnix("u6.sock") catch { case ListenFailed, Denied, Unsupported: return false } let addr = l.Addr() let tx = DialUnix(addr) catch { case DialFailed, Denied, Unsupported: return false } let rx = l.Accept() catch { case Failed, Unsupported: return false } l.Close() let dialedAfter = DialUnix(addr) catch { case DialFailed: tx.Close() let eof = rx.Read() catch { case Failed, Unsupported: return false } if eof != "" { return false } let rEOF = rx.RecvFDs(1) catch { case Failed: return true case Unsupported: return false } return rEOF.Data == "" && false case Denied, Unsupported: return false } dialedAfter.Close() false }`,
}

// TestNetLaws runs the probe with --seed 1; lawledger.Run holds that it
// restates exactly the ledger's laws whose runner is this test, each with
// the row's statement, and that each passed.
//
// It builds Go because the laws run in the built test binary (ergo test),
// the only place a law runs.
func TestNetLaws(t *testing.T) {
	needsBuild(t)
	exes := tableExes(t, "netlaws")
	if exes == nil {
		return
	}
	stdout, stderr, code := runExeArgs(t, exes["netlaws"], "--seed", "1")
	if code != 0 {
		t.Errorf("the net law tests failed: exit %d\n%s\n%s", code, stdout, firstLines(stderr, 20))
	}
	lawledger.Run(t, lawsPassed(t, netLawStatements, func(key string) string {
		pkg, law, _ := strings.Cut(key, " ")
		return stdLawsTestName(pkg, law)
	}, netLawsProbe, stdout))
}

const netUnixConfinedProbe = `import fstest
import net

type tcpCheck {
	TCPDenied() bool,
}

type tcpCheckHost { host net.Host } implements tcpCheck

func TCPDenied(u tcpCheckHost) bool {
	denyTCPListen(u.host, "127.0.0.1:0") && denyTCPDial(u.host, "127.0.0.1:0")
}

let onUnixHost = context needs fstest.TestAmbient { net.UnixNetwork: net.NewTestUnixHost(), tcpCheck: tcpCheckHost{host: net.NewTestUnixHost()} }

func denyUnixListen(path string) bool needs net.UnixNetwork {
	let l = net.UnixNetwork.ListenUnix(path) catch {
	case net.Denied as d:
		return d.Addr == path
	case net.ListenFailed, net.Unsupported:
		return false
	}
	l.Close()
	false
}

func denyUnixDial(path string) bool needs net.UnixNetwork {
	let c = net.UnixNetwork.DialUnix(path) catch {
	case net.Denied as d:
		return d.Addr == path
	case net.DialFailed, net.Unsupported:
		return false
	}
	c.Close()
	false
}

func denyTCPListen(h net.Host, addr string) bool {
	let l = h.Listen(addr) catch {
	case net.Denied as d:
		return d.Addr == addr
	case net.ListenFailed:
		return false
	}
	l.Close()
	false
}

func denyTCPDial(h net.Host, addr string) bool {
	let c = h.Dial(addr) catch {
	case net.Denied as d:
		return d.Addr == addr
	case net.DialFailed:
		return false
	}
	c.Close()
	false
}

test NetUnixHostConfined {
	with onUnixHost {
		let l = try net.UnixNetwork.ListenUnix("inside.sock")
		let c = try net.UnixNetwork.DialUnix("inside.sock")
		let s = try l.Accept()
		try c.Write("ping")
		assert try s.Read() == "ping"
		s.Close()
		c.Close()
		l.Close()
		for path in []string{"../out.sock", "/tmp/outside.sock", "@abstract", "bad\x00.sock"} {
			assert denyUnixListen(path)
			assert denyUnixDial(path)
		}
		assert tcpCheck.TCPDenied()
	}
}
`

// TestNetUnixConfined verifies NewTestUnixHost path confinement and TCP denial
// natively under fstest.TestAmbient.
func TestNetUnixConfined(t *testing.T) {
	needsBuild(t)
	exes := tableExes(t, "netunixconfined")
	if exes == nil {
		return
	}
	stdout, stderr, code := runExeArgs(t, exes["netunixconfined"], "--seed", "1")
	if code != 0 {
		t.Fatalf("netunixconfined failed: exit %d\n%s\n%s", code, stdout, firstLines(stderr, 20))
	}
	if !strings.Contains(stdout, "ok NetUnixHostConfined") {
		t.Fatalf("missing pass line for NetUnixHostConfined:\n%s", stdout)
	}
}
