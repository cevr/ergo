//go:build linux && (amd64 || arm64)

package testinputs

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"syscall"
	"testing"
	"unsafe"

	"ergo.local/prototype/internal/testwait"
)

// run interprets the filter for one call, as the kernel's classic BPF
// interpreter would on struct seccomp_data, and returns the action.
func run(t *testing.T, program []sockFilter, arch, nr uint32, args ...uint64) uint32 {
	t.Helper()
	var data [64]byte
	binary.LittleEndian.PutUint32(data[0:], nr)
	binary.LittleEndian.PutUint32(data[4:], arch)
	for i, arg := range args {
		binary.LittleEndian.PutUint64(data[16+8*i:], arg)
	}
	var a uint32
	for pc := 0; pc < len(program); pc++ {
		in := program[pc]
		switch in.code {
		case 0x20: // ld [k]
			a = binary.LittleEndian.Uint32(data[in.k:])
		case 0x15, 0x25, 0x35, 0x45: // jeq, jgt, jge, jset
			taken := a == in.k
			switch in.code {
			case 0x25:
				taken = a > in.k
			case 0x35:
				taken = a >= in.k
			case 0x45:
				taken = a&in.k != 0
			}
			if taken {
				pc += int(in.jt)
			} else {
				pc += int(in.jf)
			}
		case 0x06: // ret k
			return in.k
		default:
			t.Fatalf("instruction %d: unknown code %#x", pc, in.code)
		}
	}
	t.Fatal("the program fell off its end")
	return 0
}

func TestFilter(t *testing.T) {
	program := filter()
	if len(program) > 4096 {
		t.Fatalf("%d instructions exceed BPF_MAXINSNS", len(program))
	}
	notify := uint32(seccompRetUserNotif)
	allow := uint32(seccompRetAllow)
	for _, c := range calls {
		want := notify
		switch c.kind {
		case kindRefused:
			want = seccompRetErrno | uint32(syscall.ENOSYS)
		case kindNamespaceClone:
			if got := run(t, program, auditArch, c.nr, uint64(syscall.SIGCHLD)); got != allow {
				t.Errorf("a plain clone: action %#x, want allow", got)
			}
			want = notify
			if got := run(t, program, auditArch, c.nr, syscall.CLONE_NEWUSER|uint64(syscall.SIGCHLD)); got != want {
				t.Errorf("clone(CLONE_NEWUSER): action %#x, want notify", got)
			}
			continue
		case kindSocket:
			if got := run(t, program, auditArch, c.nr); got != allow {
				t.Errorf("%d with no address: action %#x, want allow", c.nr, got)
			}
			args := make([]uint64, 6)
			for _, address := range []uint64{0x7fff0000_00001000, 0x00001000, 0x7fff0000_00000000} {
				args[c.path] = address
				if got := run(t, program, auditArch, c.nr, args...); got != notify {
					t.Errorf("%d with address %#x: action %#x, want notify", c.nr, address, got)
				}
			}
			continue
		}
		if c.kind == kindSocketMsg || c.kind == kindSocketMsgs {
			// No descriptor or flag is let through: the observer takes the
			// listener with pidfd_getfd, so no call carries it. The shape the
			// old handover exempted (descriptor 3, MSG_CMSG_CLOEXEC), with or
			// without high halves, is notified like any other.
			for _, args := range [][]uint64{{3, 0, 0x40000000}, {1<<32 | 3, 0, 1<<32 | 0x40000000}, {3, 0, 0}} {
				if got := run(t, program, auditArch, c.nr, args...); got != notify {
					t.Errorf("%d%v: action %#x, want notify", c.nr, args, got)
				}
			}
		}
		if got := run(t, program, auditArch, c.nr, 0); got != want {
			t.Errorf("%d (kind %d): action %#x, want %#x", c.nr, c.kind, got, want)
		}
	}
	for _, nr := range []uint32{0, 1, 3, 9, 60, 63, 231, highestKnownCall} { // read, write, close, mmap, exit... on amd64; others on arm64
		if _, ok := callsByNumber[int32(nr)]; ok {
			continue
		}
		if got := run(t, program, auditArch, nr, 0); got != allow {
			t.Errorf("unlisted call %d: action %#x, want allow", nr, got)
		}
	}
	// A call newer than the table (getxattrat, open_tree_attr, file_getattr)
	// is notified, and the observer refuses what it cannot classify.
	for _, nr := range []uint32{highestKnownCall + 1, 464, 467, 468, 1023} {
		if got := run(t, program, auditArch, nr, 0); got != notify {
			t.Errorf("call %d, newer than the table: action %#x, want notify", nr, got)
		}
	}
	for _, c := range calls {
		if c.nr > highestKnownCall {
			t.Errorf("call %d (%s) lies above highestKnownCall", c.nr, c.name)
		}
	}
	// A call of another architecture (i386 on amd64, arm on arm64) is
	// notified, and the observer refuses what it cannot decode.
	if got := run(t, program, 0x40000003, 1, 0); got != notify {
		t.Errorf("foreign architecture: action %#x, want notify", got)
	}
	if checkX32SyscallBits {
		if got := run(t, program, auditArch, x32SyscallBit|1, 0); got != notify {
			t.Errorf("x32 call: action %#x, want notify", got)
		}
	}
}

// TestShebangNeverBlocks checks that reading the interpreter line of an
// executed path skips a FIFO instead of waiting for a writer, and still
// follows a script's absolute interpreter.
func TestShebangNeverBlocks(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o755); err != nil {
		t.Fatal(err)
	}
	done := make(chan string, 1)
	go func() { done <- shebang(fifo) }()
	select {
	case got := <-done:
		if got != "" {
			t.Fatalf("a FIFO named the interpreter %q", got)
		}
	case <-testwait.After(t):
		t.Fatal("reading a FIFO's interpreter line blocked")
	}
	script := filepath.Join(dir, "script")
	if err := os.WriteFile(script, []byte("#!/nonexistent/loop-probe-x -e\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := shebang(script); got != "/nonexistent/loop-probe-x" {
		t.Fatalf("the script's interpreter is %q", got)
	}
}

// TestMagicLinks checks that a path through a process's working directory,
// root or descriptor in /proc, or through /dev/fd, becomes the path of the
// file the kernel reaches.
func TestMagicLinks(t *testing.T) {
	dir := t.TempDir()
	file, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	fd := strconv.Itoa(int(file.Fd()))
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	tid := syscall.Gettid()
	pid := strconv.Itoa(os.Getpid())
	for _, c := range []struct{ path, want string }{
		{"/proc/self/cwd/a/b", cwd + "/a/b"},
		{"/proc/thread-self/cwd/a", cwd + "/a"},
		{"/proc/" + pid + "/task/" + strconv.Itoa(tid) + "/root/etc/hosts", "/etc/hosts"},
		{"/proc/self/fd/" + fd + "/x.txt", dir + "/x.txt"},
		{"/dev/fd/" + fd + "/sub/x.txt", dir + "/sub/x.txt"},
		{"/proc/self/fd/" + fd, "/proc/self/fd/" + fd}, // a reopen: the open by path was recorded
		{"/proc/self/status", "/proc/self/status"},
		{"/proc/self/fd/999999/x", "/proc/self/fd/999999/x"}, // closed: the call fails too
		{"/dev/null", "/dev/null"},
		{"/dev/stdin/x", "/dev/stdin/x"},
		{"/usr/lib/x", "/usr/lib/x"},
	} {
		if got := magic(tid, c.path); got != c.want {
			t.Errorf("magic(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

// TestLinkedDirs checks that a name relative to a directory reached through
// a link inside the root, by chdir or by opening it, is also recorded under
// the link, whichever process makes the call, and under every link that led
// there; and that nothing else is remembered.
func TestLinkedDirs(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "a")
	link := filepath.Join(base, "current")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "value.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for name, to := range map[string]string{"current": "a", "file": "a/value.txt"} {
		if err := os.Symlink(to, filepath.Join(base, name)); err != nil {
			t.Fatal(err)
		}
	}
	outside := filepath.Join(t.TempDir(), "into")
	if err := os.Symlink(target, outside); err != nil {
		t.Fatal(err)
	}
	physical, err := filepath.EvalSymlinks(link)
	if err != nil {
		t.Fatal(err)
	}
	o := newObserver(base)
	if got := o.dirs.logical(physical); got != nil {
		t.Fatalf("before any call: %q", got)
	}
	o.enter(filepath.Join(base, "missing"), false) // fails: nothing to remember
	o.enter(target, false)                         // crosses no link
	o.enter(target, true)
	o.enter("/proc/self/cwd", false)           // a magic link, which resolves in the observer
	o.enter(outside, false)                    // a link outside the root, which cmd/go does not recheck
	o.enter(filepath.Join(base, "file"), true) // an open of a file, through a link
	if o.dirs.active.Load() {
		t.Fatalf("remembered: %q", o.dirs.aliases)
	}
	o.enter(link, true) // an open of the directory, without O_DIRECTORY
	o.enter(link, false)
	if got := o.dirs.logical(physical); !slices.Equal(got, []string{link}) {
		t.Fatalf("after open and chdir: %q, want %q", got, link)
	}
	if got := o.dirs.logical(base); got != nil {
		t.Fatalf("at another physical location: %q", got)
	}
	// Any thread, a reparented child's included: nothing is kept per process
	// or per descriptor.
	const child = 1 << 30 // no such thread
	for _, tid := range []int{syscall.Gettid(), child} {
		got := o.names(tid, located{path: physical + "/value.txt", base: physical, name: "value.txt"})
		if want := []string{physical + "/value.txt", link + "/value.txt"}; !slices.Equal(got, want) {
			t.Fatalf("names in %d: %q, want %q", tid, got, want)
		}
	}
	other := filepath.Join(base, "other")
	if err := os.Symlink(target, other); err != nil {
		t.Fatal(err)
	}
	o.enter(other, false)
	if got, want := o.dirs.logical(physical), []string{link, other}; !slices.Equal(got, want) {
		t.Fatalf("after a second link: %q, want %q", got, want)
	}
}

// TestLocateKeepsTheDirectory checks that a name relative to a descriptor
// keeps the descriptor's directory, as one relative to the working directory
// does, so names can add the links that led there.
func TestLocateKeepsTheDirectory(t *testing.T) {
	dir := t.TempDir()
	d, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	physical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	a := access{tid: syscall.Gettid()}
	got, ok, err := a.locate("sub/value.txt", int(d.Fd()), false)
	want := located{path: physical + "/sub/value.txt", base: physical, name: "sub/value.txt"}
	if !ok || err != nil || got != want {
		t.Fatalf("locate = %+v, %v, %v; want %+v", got, ok, err, want)
	}
}

// TestSocketPath checks the decoding of the addresses socket calls name, read
// from this process's memory as from a notified thread's.
func TestSocketPath(t *testing.T) {
	tid := syscall.Gettid()
	address := func(family uint16, path string) []byte {
		raw := make([]byte, 2, sockaddrUnSize)
		binary.LittleEndian.PutUint16(raw, family)
		return append(raw, path...)
	}
	for _, c := range []struct {
		raw    []byte
		length uint64
		want   string
	}{
		{address(syscall.AF_UNIX, "run/agent.sock\x00"), 17, "run/agent.sock"},
		{address(syscall.AF_UNIX, "/abs/x.sock"), 13, "/abs/x.sock"},          // no NUL: the length ends it
		{address(syscall.AF_UNIX, "/abs/x.sock\x00junk"), 999, "/abs/x.sock"}, // read up to the struct's size
		{address(syscall.AF_UNIX, "\x00abstract"), 11, ""},
		{address(syscall.AF_UNIX, ""), 2, ""}, // unnamed
		{address(syscall.AF_INET, "\x00\x50\x7f\x00\x00\x01"), 16, ""},
	} {
		buffer := make([]byte, sockaddrUnSize)
		copy(buffer, c.raw)
		got, err := socketPath(tid, uint64(uintptr(unsafe.Pointer(&buffer[0]))), c.length)
		if err != nil || got != c.want {
			t.Errorf("socketPath(%q, %d) = %q, %v; want %q", c.raw, c.length, got, err, c.want)
		}
		runtime.KeepAlive(buffer)
	}
	if got, err := socketPath(tid, 0, 16); got != "" || err != nil {
		t.Errorf("a NULL address: %q, %v", got, err)
	}
	// struct msghdr: msg_name, then msg_namelen.
	header := make([]byte, 56)
	binary.LittleEndian.PutUint64(header, 0x1234)
	binary.LittleEndian.PutUint32(header[8:], 17)
	got, ok, err := messageAddress(tid, uint64(uintptr(unsafe.Pointer(&header[0]))))
	if !ok || err != nil || got != [2]uint64{0x1234, 17} {
		t.Errorf("messageAddress = %v, %v, %v", got, ok, err)
	}
	runtime.KeepAlive(header)
	// Memory the kernel faults on too names no address; the e2e case "fail
	// closed: undumpable sendmsg" covers a header the kernel can read but
	// the observer cannot.
	if got, ok, err := messageAddress(tid, 0); ok || err != nil {
		t.Errorf("messageAddress(NULL) = %v, %v, %v; want no address, no error", got, ok, err)
	}
}
