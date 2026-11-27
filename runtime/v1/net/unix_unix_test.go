//go:build unix

package net

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"ergo.local/prototype/runtime/rt"
)

func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "unx")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

type unixTransport struct {
	name   string
	listen func(path string) (*UnixListener, error)
	dial   func(t *rt.Task, path string) (*UnixConn, error)
	tcp    network
	sock   func(name string) string
}

func hostUnixTransport(t *testing.T) unixTransport {
	dir := shortTempDir(t)
	return unixTransport{
		name:   "host",
		listen: NetListenUnix,
		dial:   NetDialUnix,
		tcp:    hostNetwork(),
		sock:   func(name string) string { return filepath.Join(dir, name) },
	}
}

func pipeUnixTransport() unixTransport {
	p := NetNewPipe()
	return unixTransport{
		name:   "pipe",
		listen: p.ListenUnix,
		dial:   func(_ *rt.Task, path string) (*UnixConn, error) { return p.DialUnix(path) },
		tcp:    network{"pipe", p.Listen, func(_ *rt.Task, addr string) (*Conn, error) { return p.Dial(addr) }},
		sock:   func(name string) string { return name },
	}
}

// TestUnixTransferLiveTCPListenerFD transfers a live TCP Listener FD over
// a Unix domain socket via SendFDs / RecvFDs, closes the original
// listener, and verifies the receiver accepts both queued pre-cutover
// connections and fresh post-cutover connections.
func TestUnixTransferLiveTCPListenerFD(t *testing.T) {
	for _, mk := range []func() unixTransport{
		func() unixTransport { return hostUnixTransport(t) },
		pipeUnixTransport,
	} {
		tr := mk()
		t.Run(tr.name, func(t *testing.T) {
			task := rt.Root("cutover")
			tcpLn, err := tr.tcp.listen("127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			tcpAddr := tcpLn.Addr()

			earlyClient, err := tr.tcp.dial(task, tcpAddr)
			if err != nil {
				tcpLn.Close()
				t.Fatal(err)
			}
			defer earlyClient.Close()
			if err := earlyClient.Write(task, "early-before-cutover"); err != nil {
				tcpLn.Close()
				t.Fatal(err)
			}

			ctlLn, err := tr.listen(tr.sock("ctl.sock"))
			if err != nil {
				tcpLn.Close()
				t.Fatal(err)
			}
			defer ctlLn.Close()

			tx, err := tr.dial(task, ctlLn.Addr())
			if err != nil {
				tcpLn.Close()
				t.Fatal(err)
			}
			defer tx.Close()
			rx, err := ctlLn.Accept(task)
			if err != nil {
				tcpLn.Close()
				t.Fatal(err)
			}
			defer rx.Close()

			fd, err := tcpLn.DupFD()
			if err != nil {
				tcpLn.Close()
				t.Fatal(err)
			}
			if err := tx.SendFDs(task, "tcp-cutover", []*FD{fd}); err != nil {
				fd.Close()
				tcpLn.Close()
				t.Fatal(err)
			}
			fd.Close()
			tcpLn.Close()

			msg, err := rx.RecvFDs(task, 4)
			if err != nil {
				t.Fatal(err)
			}
			if msg.Data != "tcp-cutover" || len(msg.FDs) != 1 {
				for _, rf := range msg.FDs {
					rf.Close()
				}
				t.Fatalf("got (%q, %d fds), want (%q, 1)", msg.Data, len(msg.FDs), "tcp-cutover")
			}
			newLn, err := msg.FDs[0].IntoListener()
			if err != nil {
				t.Fatal(err)
			}
			defer newLn.Close()

			s1, err := newLn.Accept(task)
			if err != nil {
				t.Fatal(err)
			}
			defer s1.Close()
			got1, err := s1.Read(task)
			if err != nil || got1 != "early-before-cutover" {
				t.Fatalf("early read = (%q, %v), want %q", got1, err, "early-before-cutover")
			}
			if err := s1.Write(task, "ack-early"); err != nil {
				t.Fatal(err)
			}
			ack1, err := earlyClient.Read(task)
			if err != nil || ack1 != "ack-early" {
				t.Fatalf("early ack = (%q, %v), want %q", ack1, err, "ack-early")
			}

			lateClient, err := tr.tcp.dial(task, tcpAddr)
			if err != nil {
				t.Fatal(err)
			}
			defer lateClient.Close()
			s2, err := newLn.Accept(task)
			if err != nil {
				t.Fatal(err)
			}
			defer s2.Close()
			if err := lateClient.Write(task, "late-after-cutover"); err != nil {
				t.Fatal(err)
			}
			got2, err := s2.Read(task)
			if err != nil || got2 != "late-after-cutover" {
				t.Fatalf("late read = (%q, %v), want %q", got2, err, "late-after-cutover")
			}
		})
	}
}

// TestUnixTransferOSPipeAndDup tests passing an active os.Pipe() FD via
// NetAdoptFD + SendFDs + RecvFDs, closing original handles, and testing FD.Dup.
func TestUnixTransferOSPipeAndDup(t *testing.T) {
	dir := shortTempDir(t)
	task := rt.Root("pipefd")

	ctlLn, err := NetListenUnix(filepath.Join(dir, "p.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer ctlLn.Close()

	tx, err := NetDialUnix(task, ctlLn.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Close()
	rx, err := ctlLn.Accept(task)
	if err != nil {
		t.Fatal(err)
	}
	defer rx.Close()

	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	adopted, err := NetAdoptFD(int(pr.Fd()))
	_ = pr.Close()
	if err != nil {
		_ = pw.Close()
		t.Fatal(err)
	}
	duped, err := adopted.Dup()
	if err != nil {
		adopted.Close()
		_ = pw.Close()
		t.Fatal(err)
	}
	adopted.Close()
	if duped.Fd() < 0 {
		duped.Close()
		_ = pw.Close()
		t.Fatal("duped.Fd() < 0")
	}

	if err := tx.SendFDs(task, "pipe-read", []*FD{duped}); err != nil {
		duped.Close()
		_ = pw.Close()
		t.Fatal(err)
	}
	duped.Close()

	msg, err := rx.RecvFDs(task, 4)
	if err != nil {
		_ = pw.Close()
		t.Fatal(err)
	}
	if msg.Data != "pipe-read" || len(msg.FDs) != 1 {
		for _, f := range msg.FDs {
			f.Close()
		}
		_ = pw.Close()
		t.Fatalf("got (%q, %d fds)", msg.Data, len(msg.FDs))
	}
	recvFD := msg.FDs[0]
	defer recvFD.Close()

	adoptedRecv, err := NetAdoptFD(recvFD.Fd())
	if err != nil {
		_ = pw.Close()
		t.Fatal(err)
	}
	defer adoptedRecv.Close()
	recvFD.Close()

	if _, err := pw.WriteString("hello-via-pipe-fd"); err != nil {
		_ = pw.Close()
		t.Fatal(err)
	}
	_ = pw.Close()

	buf := make([]byte, 64)
	n, err := syscall.Read(adoptedRecv.Fd(), buf)
	if err != nil {
		t.Fatalf("reading transferred pipe fd: %v", err)
	}
	if string(buf[:n]) != "hello-via-pipe-fd" {
		t.Fatalf("got %q, want %q", string(buf[:n]), "hello-via-pipe-fd")
	}
}

// TestUnixListenerUnlinkAndFraming verifies SetUnlinkOnClose behavior,
// consecutive framed SendFDs FIFO ordering (including empty payloads),
// type mismatch checks on Into*, and bounded RecvFDs rejection.
func TestUnixListenerUnlinkAndFraming(t *testing.T) {
	for _, mk := range []func() unixTransport{
		func() unixTransport { return hostUnixTransport(t) },
		pipeUnixTransport,
	} {
		tr := mk()
		t.Run(tr.name, func(t *testing.T) {
			task := rt.Root("framing")
			ch, err := tr.listen(tr.sock("ch.sock"))
			if err != nil {
				t.Fatal(err)
			}
			defer ch.Close()
			tx, err := tr.dial(task, ch.Addr())
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Close()
			rx, err := ch.Accept(task)
			if err != nil {
				t.Fatal(err)
			}
			defer rx.Close()

			targetPath := tr.sock("tgt.sock")
			tgt, err := tr.listen(targetPath)
			if err != nil {
				t.Fatal(err)
			}
			if dupLn, err := tr.listen(targetPath); err == nil {
				dupLn.Close()
				tgt.Close()
				t.Fatal("duplicate ListenUnix succeeded, want error")
			}
			tgt.SetUnlinkOnClose(false)
			tfd, err := tgt.DupFD()
			if err != nil {
				tgt.Close()
				t.Fatal(err)
			}

			tcpLn, err := tr.tcp.listen("127.0.0.1:0")
			if err != nil {
				tfd.Close()
				tgt.Close()
				t.Fatal(err)
			}
			defer tcpLn.Close()
			tcpFD, err := tcpLn.DupFD()
			if err != nil {
				tfd.Close()
				tgt.Close()
				t.Fatal(err)
			}

			// Send 3 back-to-back framed messages without coalescing.
			if err := tx.SendFDs(task, "", []*FD{tfd}); err != nil {
				t.Fatal(err)
			}
			if err := tx.SendFDs(task, "middle-empty", nil); err != nil {
				t.Fatal(err)
			}
			if err := tx.SendFDs(task, "two-fds", []*FD{tfd, tcpFD}); err != nil {
				t.Fatal(err)
			}
			tfd.Close()
			tcpFD.Close()
			tgt.Close() // path preserved because SetUnlinkOnClose(false)

			m1, err := rx.RecvFDs(task, 4)
			if err != nil || m1.Data != "" || len(m1.FDs) != 1 {
				t.Fatalf("m1 = (%+v, %v)", m1, err)
			}
			// Type mismatch: Unix listener FD into TCP Listener must fail & consume FD.
			dupForMismatch, err := m1.FDs[0].Dup()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := dupForMismatch.IntoListener(); err == nil {
				t.Fatal("IntoListener on UnixListener FD succeeded, want error")
			}

			tgt2, err := m1.FDs[0].IntoUnixListener()
			if err != nil {
				t.Fatal(err)
			}

			m2, err := rx.RecvFDs(task, 0)
			if err != nil || m2.Data != "middle-empty" || len(m2.FDs) != 0 {
				t.Fatalf("m2 = (%+v, %v)", m2, err)
			}

			// Bounded control check: 2 FDs sent when maxFDs=1 must fail cleanly.
			if bad, err := rx.RecvFDs(task, 1); err == nil {
				for _, f := range bad.FDs {
					f.Close()
				}
				t.Fatal("RecvFDs(1) succeeded with 2 FDs in flight, want error")
			}

			// Closing an adopted UnixListener without SetUnlinkOnClose(true)
			// must preserve the socket path so live peers continue accepting.
			keepDup, err := tgt2.DupFD()
			if err != nil {
				tgt2.Close()
				t.Fatal(err)
			}
			keepLn, err := keepDup.IntoUnixListener()
			if err != nil {
				tgt2.Close()
				t.Fatal(err)
			}
			keepLn.Close()

			// Transferred listener still accepts dials while old tgt is closed.
			c, err := tr.dial(task, targetPath)
			if err != nil {
				tgt2.Close()
				t.Fatal(err)
			}
			defer c.Close()
			s, err := tgt2.Accept(task)
			if err != nil {
				tgt2.Close()
				t.Fatal(err)
			}
			defer s.Close()
			if err := c.Write(task, "ok"); err != nil {
				t.Fatal(err)
			}
			got, err := s.Read(task)
			if err != nil || got != "ok" {
				t.Fatalf("got (%q, %v), want ok", got, err)
			}

			tgt2.SetUnlinkOnClose(true)
			tgt2.Close()
			if late, err := tr.dial(task, targetPath); err == nil {
				late.Close()
				t.Fatal("dial after SetUnlinkOnClose(true)+Close succeeded, want error")
			}

			// Invalid bounds & invalid adopt checks.
			if _, err := rx.RecvFDs(task, -1); err == nil {
				t.Fatal("RecvFDs(-1) succeeded")
			}
			if _, err := rx.RecvFDs(task, MaxControlFDs+1); err == nil {
				t.Fatal("RecvFDs(MaxControlFDs+1) succeeded")
			}
			if _, err := NetAdoptFD(-1); err == nil {
				t.Fatal("NetAdoptFD(-1) succeeded")
			}
		})
	}
}

// TestNetUnixStopEndsWaits mirrors TestNetStopEndsWaits for UnixListener
// Accept and UnixConn Read, Write, and RecvFDs across Host and PipeNet.
func TestNetUnixStopEndsWaits(t *testing.T) {
	for _, mk := range []func() unixTransport{
		func() unixTransport { return hostUnixTransport(t) },
		pipeUnixTransport,
	} {
		tr := mk()
		for _, wait := range []string{"accept", "read", "write", "recvfds"} {
			t.Run(tr.name+"/"+wait, func(t *testing.T) {
				l, err := tr.listen(tr.sock("stop.sock"))
				if err != nil {
					t.Fatal(err)
				}
				defer l.Close()
				owner := rt.Root("owner")
				var c, s *UnixConn
				if wait != "accept" {
					if c, err = tr.dial(owner, l.Addr()); err != nil {
						t.Fatal(err)
					}
					defer c.Close()
					if s, err = l.Accept(owner); err != nil {
						t.Fatal(err)
					}
					defer s.Close()
				}
				task := rt.Root("stopped " + wait)
				waiting := make(chan struct{})
				done := make(chan any)
				go func() {
					defer func() { done <- recover() }()
					cancel := rt.WhenWaiting(task, func() { close(waiting) })
					defer cancel()
					switch wait {
					case "accept":
						_, err = l.Accept(task)
					case "read":
						_, err = s.Read(task)
					case "recvfds":
						_, err = s.RecvFDs(task, 2)
					case "write":
						err = c.Write(task, strings.Repeat("x", 64<<20))
					}
				}()
				var q any
				select {
				case <-waiting:
					task.Interrupt()
					q = <-done
				case q = <-done:
					t.Fatalf("the %s returned without waiting: %v (err %v)", wait, q, err)
				}
				if q != rt.Interrupted {
					t.Fatalf("stopped %s ended with %v (err %v), want rt.Interrupted", wait, q, err)
				}
				if wait != "read" && wait != "recvfds" {
					return
				}
				if wait == "read" {
					if err := c.Write(owner, "after"); err != nil {
						t.Fatal(err)
					}
					got, err := s.Read(rt.Root("reader"))
					if err != nil || got != "after" {
						t.Fatalf("read after stop = (%q, %v), want after", got, err)
					}
					return
				}
				if err := c.SendFDs(owner, "after-fd", nil); err != nil {
					t.Fatal(err)
				}
				got, err := s.RecvFDs(rt.Root("reader"), 2)
				if err != nil || got.Data != "after-fd" {
					t.Fatalf("recvfds after stop = (%+v, %v)", got, err)
				}
			})
		}
	}
}

// TestNetConfineUnix verifies path confinement, symlink escape rejection,
// abstract namespace rejection, and NUL byte rejection for NewTestUnixHost.
func TestNetConfineUnix(t *testing.T) {
	root := shortTempDir(t)
	outside := shortTempDir(t)
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}

	okCases := []string{
		"s.sock",
		"sub/s.sock",
		filepath.Join(root, "abs.sock"),
		"",
	}
	for _, p := range okCases {
		if _, err := NetConfineUnix(root, p); err != nil {
			t.Errorf("NetConfineUnix(%q, %q) = %v, want nil", root, p, err)
		}
	}

	denyCases := []string{
		"../out.sock",
		filepath.Join(outside, "out.sock"),
		"escape/out.sock",
		"@abstract",
		"bad\x00.sock",
	}
	for _, p := range denyCases {
		if got, err := NetConfineUnix(root, p); err == nil {
			t.Errorf("NetConfineUnix(%q, %q) = (%q, nil), want Denied", root, p, got)
		}
	}
}
