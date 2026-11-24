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
`

// netLawStatements is the statement of each law the probe restates, by
// ledger key; lawledger.Run holds it equal to the ledger.
var netLawStatements = map[string]string{
	"net Network.WriteThenReadRoundTrips": `WriteThenReadRoundTrips(data string, reply string) { let l = Listen("127.0.0.1:0") catch { case ListenFailed, Denied: return false } let c = Dial(l.Addr()) catch { case DialFailed, Denied: return false } let s = l.Accept() catch { case Failed: return false } sends(c, s, data) && sends(s, c, reply) }`,
	"net Network.CloseEndsReads":          `CloseEndsReads(data string) { let l = Listen("127.0.0.1:0") catch { case ListenFailed, Denied: return false } let c = Dial(l.Addr()) catch { case DialFailed, Denied: return false } let s = l.Accept() catch { case Failed: return false } c.Write(data) catch { case Failed: return false } c.Close() let got = ReadAll(s) catch { case Failed: return false } got == data }`,
	"net Network.DialAfterCloseFails":     `DialAfterCloseFails() { let l = Listen("127.0.0.1:0") catch { case ListenFailed, Denied: return false } let addr = l.Addr() l.Close() let c = Dial(addr) catch { case DialFailed: return true case Denied: return false } c.Close() false }`,
	"net Network.AcceptsInDialOrder":      `AcceptsInDialOrder(a string, b string) { let l = Listen("127.0.0.1:0") catch { case ListenFailed, Denied: return false } let first = Dial(l.Addr()) catch { case DialFailed, Denied: return false } let second = Dial(l.Addr()) catch { case DialFailed, Denied: return false } let x = l.Accept() catch { case Failed: return false } let y = l.Accept() catch { case Failed: return false } sends(first, x, a + "1") && sends(second, y, b + "2") }`,
	"net Network.ListenTakenFails":        `ListenTakenFails() { let l = Listen("127.0.0.1:0") catch { case ListenFailed, Denied: return false } let again = Listen(l.Addr()) catch { case ListenFailed: return true case Denied: return false } again.Close() false }`,
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
