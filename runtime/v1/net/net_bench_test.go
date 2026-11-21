package net

import (
	"context"
	"io"
	gonet "net"
	"testing"
	"time"

	"ergo.local/prototype/runtime/rt"
)

// BenchmarkNetRoundTrip is one 64-byte message and its echo per op, on a
// connected pair: through the bodies on the host's loopback (ergo-host),
// through hand Go with the same guarantee, a read and a write that end on
// a cancel, by context.AfterFunc moving the deadline (go-host, the
// control), and through a PipeNet (ergo-pipe). Count it with
// perf stat -e instructions:u over a fixed -benchtime=Nx (TENTH OF GO).
func BenchmarkNetRoundTrip(b *testing.B) {
	msg := string(make([]byte, 64))
	b.Run("ergo-host", func(b *testing.B) {
		l, _ := NetListen("127.0.0.1:0")
		defer l.Close()
		task := rt.Root("bench")
		c, _ := NetDial(task, l.Addr())
		defer c.Close()
		s, _ := l.Accept(task)
		defer s.Close()
		echo(b, task, c, s, msg)
	})
	b.Run("ergo-pipe", func(b *testing.B) {
		p := NetNewPipe()
		l, _ := p.Listen("127.0.0.1:0")
		defer l.Close()
		task := rt.Root("bench")
		c, _ := p.Dial(l.Addr())
		defer c.Close()
		s, _ := l.Accept(task)
		defer s.Close()
		echo(b, task, c, s, msg)
	})
	b.Run("go-host", func(b *testing.B) {
		ln, _ := gonet.Listen("tcp", "127.0.0.1:0")
		defer ln.Close()
		c, _ := gonet.Dial("tcp", ln.Addr().String())
		defer c.Close()
		s, _ := ln.Accept()
		defer s.Close()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		buf := make([]byte, readMax)
		b.ResetTimer()
		for range b.N {
			if err := cancelable(ctx, c.SetWriteDeadline, func() error { _, err := io.WriteString(c, msg); return err }); err != nil {
				b.Fatal(err)
			}
			for got := 0; got < len(msg); {
				var n int
				err := cancelable(ctx, s.SetReadDeadline, func() (err error) { n, err = s.Read(buf); return err })
				if err != nil {
					b.Fatal(err)
				}
				got += n
			}
			_ = string(buf[:len(msg)])
		}
	})
}

// cancelable runs call with a cancel of ctx moving its deadline into the
// past, the hand-Go form of an interruptible blocking call.
func cancelable(ctx context.Context, set func(time.Time) error, call func() error) error {
	stop := context.AfterFunc(ctx, func() { _ = set(past) })
	err := call()
	if !stop() {
		_ = set(time.Time{})
	}
	return err
}

func echo(b *testing.B, task *rt.Task, c, s *Conn, msg string) {
	b.ResetTimer()
	for range b.N {
		if err := c.Write(task, msg); err != nil {
			b.Fatal(err)
		}
		for got := 0; got < len(msg); {
			part, err := s.Read(task)
			if err != nil {
				b.Fatal(err)
			}
			got += len(part)
		}
	}
}
