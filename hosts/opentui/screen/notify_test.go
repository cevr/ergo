package screen

import (
	"bytes"
	"context"
	"io"
	"os"
	"testing"
)

// TestNotifyReachesTheTerminal opens a terminal screen on a pipe pair (no
// TTY) and answers the setup's version query as iTerm2 does: Notify then
// writes the text as an OSC 9 notification. Unanswered (a terminal that
// names no notification support), it writes none. Either way Said keeps
// the text. The reply is read before Notify: the key typed after it comes
// back through Next first.
//
// Mutants: the reply is not handed to the session (parse drops it), and
// Notify keeps the text but writes nothing.
func TestNotifyReachesTheTerminal(t *testing.T) {
	for _, reply := range []string{"\x1bP>|iTerm2 3.5.0\x1b\\", ""} {
		inR, inW, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		outR, outW, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		read := make(chan struct{})
		go func() {
			io.Copy(&out, outR)
			close(read)
		}()
		s, err := OpenOn(inR, outW, 40, 10)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := inW.Write([]byte(reply + "x")); err != nil {
			t.Fatal(err)
		}
		in, err := s.Next(context.Background())
		if err != nil || in.Kind != Key || in.Text != "x" {
			t.Fatalf("reply %q: the key after it came back as %+v, %v", reply, in, err)
		}
		if err := s.Notify("saved: 3 items"); err != nil {
			t.Fatal(err)
		}
		said := s.Said()
		s.Quit()
		outW.Close()
		inW.Close()
		<-read
		inR.Close()
		if len(said) != 1 || said[0] != "saved: 3 items" {
			t.Errorf("reply %q: Said is %q", reply, said)
		}
		wrote := bytes.Contains(out.Bytes(), []byte("\x1b]9;saved: 3 items"))
		if want := reply != ""; wrote != want {
			t.Errorf("reply %q: the OSC 9 notification written: %v, want %v", reply, wrote, want)
		}
	}
}
