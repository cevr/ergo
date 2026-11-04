package replay

import (
	"fmt"
	"math/rand/v2"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestNonRegularInputsRefuseReplay is a property over generated states of
// a FIFO in the checkout, in each class, that keeps its inode: bytes
// buffered, partly read, drained, buffered again. What a step reads from a
// FIFO is its buffer, and no stat field holds it, so the key cannot see
// it. After every operation the facts must be unreplayable and name the
// FIFO; a gate at those facts must not replay a record that sits at their
// key (written, for example, by an older gate), and a passed run must not
// record. A unix socket, the other file type a test can make, is checked
// the same way. (Round-1 finding B2: a drained FIFO replayed a pass that
// read "good".)
func TestNonRegularInputsRefuseReplay(t *testing.T) {
	const sequences = 40
	checked := 0
	for seed := uint64(1); seed <= sequences; seed++ {
		for _, class := range classes {
			n, err := checkFIFOSequence(t, seed, class)
			if err != nil {
				t.Errorf("seed %d %s: %v", seed, class, err)
			}
			checked += n
		}
	}
	for _, class := range classes {
		if err := checkSocket(t, class); err != nil {
			t.Errorf("socket %s: %v", class, err)
		}
	}
	t.Logf("%d FIFO states over %d sequences in %d classes, and a socket", checked, sequences, len(classes))
}

// checkFIFOSequence makes a checkout with a FIFO "stream", holds it open
// for reading and writing (no reader or writer is then missing, and the
// inode stays), and applies generated operations to its buffer.
func checkFIFOSequence(t *testing.T, seed uint64, class Class) (int, error) {
	root := filepath.Join(t.TempDir(), "checkout")
	mkdir(root)
	rng := rand.New(rand.NewPCG(seed, 0xf1f0))
	for i := range 1 + rng.IntN(3) {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("file%d", i)), []byte(pick(rng, contents)), 0o644); err != nil {
			return 0, err
		}
	}
	name := filepath.Join(root, "stream")
	mkfifo(name)
	pipe, err := os.OpenFile(name, os.O_RDWR|syscall.O_NONBLOCK, 0)
	if err != nil {
		return 0, err
	}
	defer pipe.Close()
	var inode uint64
	if info, err := os.Lstat(name); err == nil {
		inode = info.Sys().(*syscall.Stat_t).Ino
	}
	classify := func(string) Class { return class }
	buffered := 0
	states := 0
	for op := range 1 + rng.IntN(8) {
		var did string
		switch rng.IntN(4) {
		case 0:
			bytes := 1 + rng.IntN(16)
			if _, err := pipe.Write([]byte(strings.Repeat("g", bytes))); err != nil {
				return states, err
			}
			buffered += bytes
			did = fmt.Sprintf("buffer %d bytes", bytes)
		case 1:
			if buffered > 0 {
				want := 1 + rng.IntN(buffered)
				got, err := pipe.Read(make([]byte, want))
				if err != nil {
					return states, err
				}
				buffered -= got
				did = fmt.Sprintf("read %d bytes", got)
			} else {
				did = "read nothing (empty)"
			}
		case 2:
			for buffered > 0 {
				got, err := pipe.Read(make([]byte, buffered))
				if err != nil {
					return states, err
				}
				buffered -= got
			}
			did = "drain"
		default:
			did = "nothing"
		}
		if info, err := os.Lstat(name); err != nil || info.Sys().(*syscall.Stat_t).Ino != inode {
			return states, fmt.Errorf("op %d (%s): the FIFO's inode changed; the property needs it kept", op, did)
		}
		if err := refusesReplay(t, root, classify, "stream is a FIFO"); err != nil {
			return states, fmt.Errorf("op %d (%s, %d bytes buffered): %v", op, did, buffered, err)
		}
		states++
	}
	return states, nil
}

func checkSocket(t *testing.T, class Class) error {
	root := filepath.Join(t.TempDir(), "c")
	mkdir(root)
	// The socket sits in the checkout, whose path can be long (the gate's
	// step TMPDIR): bind it by a relative name from the checkout, since a
	// unix socket's address holds at most 104 bytes on darwin (108 on
	// Linux). Walk reaches it by its full path, which has no such bound.
	t.Chdir(root)
	listener, err := net.Listen("unix", "s")
	if err != nil {
		return err
	}
	defer listener.Close()
	return refusesReplay(t, root, func(string) Class { return class }, "s is a socket")
}

// refusesReplay collects the facts of root and checks that they cannot key
// a replay (the problem names the entry by want), that a record at their
// key does not replay, and that a passed run at them records nothing.
func refusesReplay(t *testing.T, root string, classify Classifier, want string) error {
	collect := func() (*Facts, error) {
		entries, recent, unreplayable, err := Walk(root, classify, time.Now().Add(time.Hour))
		if err != nil {
			return nil, err
		}
		return &Facts{Root: root, Entries: entries, Unreplayable: unreplayable, Recent: recent}, nil
	}
	facts, err := collect()
	if err != nil {
		return err
	}
	if !strings.Contains(strings.Join(facts.Unreplayable, "\n"), want) {
		return fmt.Errorf("the facts can key a replay (problems %q); want a problem %q", facts.Unreplayable, want)
	}
	return refuses(t, facts, collect)
}

// refuses checks that unreplayable facts refuse both ways: a passed run at
// them records nothing, and a record at their key (an older gate's) does
// not replay, for their first problem.
func refuses(t *testing.T, facts *Facts, collect func() (*Facts, error)) error {
	if facts.Problem() == "" {
		return fmt.Errorf("the facts can key a replay")
	}
	store := Open(t.TempDir())
	// A passed run at these facts records nothing.
	run := store.Begin(facts)
	object, err := run.Output()
	if err != nil {
		return err
	}
	_, _ = object.Write([]byte("passed\n"))
	run.Passed("reads the stream", object, "ran", 1, time.Now())
	if line := run.Finish(true, collect, Meta{Now: time.Now()}); !strings.Contains(line, "no whole-gate record: ") {
		return fmt.Errorf("a passed run said %q, want no record", line)
	}
	if _, err := store.Load(facts.Key()); err == nil {
		return fmt.Errorf("a passed run recorded W(K)")
	}
	// A record at the key (an older gate's) does not replay.
	stored, err := store.NewObject()
	if err != nil {
		return err
	}
	_, _ = stored.Write([]byte("passed\n"))
	sum, size, err := stored.commit()
	if err != nil {
		return err
	}
	record := Record{Version: Version, Key: facts.Key(), Recorded: time.Now().UTC(),
		Steps: []Step{{Name: "reads the stream", Object: sum, Bytes: size, Outcome: "ran", Passed: time.Now()}}}
	if err := store.write(record); err != nil {
		return err
	}
	replay, reason := store.TryReplay(facts, func() (*os.File, error) { return os.CreateTemp(t.TempDir(), "spool") })
	if replay != nil {
		replay.Close()
		return fmt.Errorf("a record at the key replayed")
	}
	if reason != "no whole-gate replay: "+facts.Problem() {
		return fmt.Errorf("no replay, but for %q; want the problem %q", reason, facts.Problem())
	}
	return nil
}
