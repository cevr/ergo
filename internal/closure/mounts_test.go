package closure

import (
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The sandbox's mounts, as /proc/self/mountinfo lists them (proc(5)), and
// the rule they must meet (checkMounts). These are plain functions of the
// mountinfo text, so their tests run everywhere, in the gate too.

// mount is one line of /proc/self/mountinfo.
type mount struct {
	id       int
	dev      string // major:minor of the file system
	root     string // the directory of the file system the mount shows
	point    string // where it is mounted, in this namespace
	readOnly bool   // the per-mount flag, not the file system's
	fsType   string
}

func parseMountinfo(data string) ([]mount, error) {
	var out []mount
	for line := range strings.SplitSeq(data, "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 6 {
			return nil, fmt.Errorf("mountinfo line %q: too few fields", line)
		}
		id, err := strconv.Atoi(fields[0])
		if err != nil {
			return nil, fmt.Errorf("mountinfo line %q: %v", line, err)
		}
		options := strings.Split(fields[5], ",")
		// The optional fields end at "-"; the file system type follows.
		sep := slices.Index(fields[6:], "-")
		if sep < 0 || 6+sep+1 >= len(fields) {
			return nil, fmt.Errorf("mountinfo line %q: no file system type", line)
		}
		out = append(out, mount{
			id:       id,
			dev:      fields[2],
			root:     unescapeMountinfo(fields[3]),
			point:    unescapeMountinfo(fields[4]),
			readOnly: options[0] == "ro",
			fsType:   fields[6+sep+1],
		})
	}
	return out, nil
}

// unescapeMountinfo decodes the octal escapes (\040 and the like) the
// kernel writes for space, tab, newline and backslash in mountinfo.
func unescapeMountinfo(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) && isOctal(s[i+1]) && isOctal(s[i+2]) && isOctal(s[i+3]) {
			b.WriteByte((s[i+1]-'0')<<6 | (s[i+2]-'0')<<3 | (s[i+3] - '0'))
			i += 3
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func isOctal(c byte) bool { return '0' <= c && c <= '7' }

// below reports whether p is dir or lies below it, for clean absolute
// paths.
func below(p, dir string) bool {
	return p == dir || dir == "/" || strings.HasPrefix(p, dir+"/")
}

// sharedMemory is where POSIX shared memory lives. The sandbox mounts a
// fresh, empty tmpfs there, a scratch directory of the run.
const sharedMemory = "/dev/shm"

// kernelWrite is a kernel control file system a package's tests must
// write, with the reason. Only cgroup2 is admitted: it holds no regular
// file, and link, rename and symlink fail in it, so it cannot give a
// checkout file a name, or hold a copy of one.
type kernelWrite struct {
	point, reason string
}

// kernelFSType is the one file system type a kernelWrite may name.
const kernelFSType = "cgroup2"

// declaredKernelWrites are the kernel control file systems that stay
// writable in a package's sandbox.
var declaredKernelWrites = map[string][]kernelWrite{
	"scripts/gate": {
		{"/sys/fs/cgroup", "the memory-budget tests (budget_scope_linux_test.go, gate-speed2's) start systemd scopes and write their cgroup limits; the systemd user manager makes the scopes outside the sandbox"},
	},
}

// checkMounts is the rule of the sandbox's mount table:
//
//   - S1: every mount is read-only, except one writable mount at each
//     scratch directory, the sandbox's tmpfs at /dev/shm, and each
//     declared cgroup2 mount (Kernel);
//   - S2: each scratch directory and /dev/shm is such a mount;
//   - no other view of the checkout: no mount lies inside the checkout, and
//     no mount other than the one that holds it shows the checkout's
//     directory of its file system or one above it (a bind alias), so the
//     checkout's path is the only name of each of its directories.
func checkMounts(mounts []mount, sb sandbox) error {
	writable := map[string]int{}
	var holder *mount
	for i, m := range mounts {
		if !m.readOnly {
			switch {
			case scratchDir(sb, m.point):
				writable[m.point]++
			case m.point == sharedMemory && m.fsType == "tmpfs":
				writable[m.point]++
			case slices.Contains(sb.Kernel, m.point) && m.fsType == kernelFSType:
			default:
				return fmt.Errorf("the mount at %s (%s %s %s) is writable", m.point, m.fsType, m.dev, m.root)
			}
		}
		if below(sb.Root, m.point) && (holder == nil || len(m.point) >= len(holder.point)) {
			holder = &mounts[i]
		}
	}
	for _, dir := range append(slices.Clone(sb.Scratch), sharedMemory) {
		if writable[dir] != 1 {
			return fmt.Errorf("the scratch directory %s has %d writable mounts, want 1", dir, writable[dir])
		}
	}
	if holder == nil {
		return fmt.Errorf("no mount holds the checkout %s", sb.Root)
	}
	inner := path.Join(holder.root, strings.TrimPrefix(sb.Root, holder.point))
	for _, m := range mounts {
		if m.id == holder.id {
			continue
		}
		if below(m.point, sb.Root) {
			return fmt.Errorf("a mount lies inside the checkout at %s: its files may have other names, which the sweep cannot classify", m.point)
		}
		if m.dev == holder.dev && (below(m.root, inner) || below(inner, m.root)) {
			return fmt.Errorf("the mount at %s shows the checkout again (%s %s): reads through it would not be classified", m.point, m.dev, m.root)
		}
	}
	return nil
}

func scratchDir(sb sandbox, dir string) bool {
	for _, s := range sb.Scratch {
		if s == dir {
			return true
		}
	}
	return false
}

// TestCheckMounts: the mount table rule, on tables that hold each way a
// sandbox can be wrong, and the one that is right.
func TestCheckMounts(t *testing.T) {
	sb := sandbox{Root: "/home/u/src/m", Scratch: []string{"/tmp/ec1/t", "/tmp/ec1/c"}, Kernel: []string{"/sys/fs/cgroup"}}
	base := []string{
		"23 1 254:0 / / ro,relatime shared:1 - ext4 /dev/root rw",
		"25 23 0:22 / /proc ro,relatime - proc proc rw",
		"40 23 0:33 / /workspaces ro,noatime - btrfs /dev/loop0 rw",
		"90 23 254:0 /tmp/ec1/t /tmp/ec1/t rw,relatime - ext4 /dev/root rw",
		"91 23 254:0 /tmp/ec1/c /tmp/ec1/c rw,relatime - ext4 /dev/root rw",
		"27 24 0:24 / /dev/shm ro,nosuid,nodev,relatime shared:4 - tmpfs shm rw",
		"92 27 0:60 / /dev/shm rw,nosuid,nodev - tmpfs tmpfs rw,mode=1777",
	}
	cases := []struct {
		name  string
		extra []string
		drop  int // index of a base line to leave out, or -1
		want  string
	}{
		{"sandbox", nil, -1, ""},
		{"a writable mount", []string{"95 23 0:40 / /data rw - tmpfs tmpfs rw"}, -1, "/data (tmpfs 0:40 /) is writable"},
		{"the root file system writable", []string{"96 1 254:0 / / rw - ext4 /dev/root rw"}, 0, "the mount at / (ext4 254:0 /) is writable"},
		{"a writable bind alias of the checkout", []string{"97 23 254:0 /home/u/src/m /tmp/alias rw - ext4 /dev/root rw"}, -1, "/tmp/alias (ext4 254:0 /home/u/src/m) is writable"},
		{"the declared cgroup2 mount writable", []string{"29 28 0:26 / /sys/fs/cgroup rw,relatime shared:7 - cgroup2 cgroup2 rw"}, -1, ""},
		{"an undeclared cgroup2 mount writable", []string{"29 28 0:26 / /mnt/cg rw - cgroup2 cgroup2 rw"}, -1, "/mnt/cg (cgroup2 0:26 /) is writable"},
		{"another file system at the declared point", []string{"29 28 0:41 / /sys/fs/cgroup rw - tmpfs tmpfs rw"}, -1, "/sys/fs/cgroup (tmpfs 0:41 /) is writable"},
		{"a read-only bind alias of the checkout", []string{"97 23 254:0 /home/u/src/m /tmp/alias ro - ext4 /dev/root rw"}, -1, "shows the checkout again"},
		{"a bind alias of a directory in the checkout", []string{"97 23 254:0 /home/u/src/m/docs /tmp/docs ro - ext4 /dev/root rw"}, -1, "shows the checkout again"},
		{"a bind alias of an ancestor", []string{"97 23 254:0 /home /mnt/home ro - ext4 /dev/root rw"}, -1, "shows the checkout again"},
		{"a second mount of the file system", []string{"97 23 254:0 / /mnt/root ro - ext4 /dev/root rw"}, -1, "shows the checkout again"},
		{"a mount inside the checkout", []string{"98 23 0:41 / /home/u/src/m/cache ro - tmpfs tmpfs rw"}, -1, "inside the checkout at /home/u/src/m/cache"},
		{"a scratch directory not mounted", nil, 4, "/tmp/ec1/c has 0 writable mounts"},
		{"no fresh /dev/shm", nil, 6, "/dev/shm has 0 writable mounts"},
		{"the host's /dev/shm writable", []string{"27 24 0:24 / /dev/shm rw,nosuid,nodev - tmpfs shm rw"}, 5, "/dev/shm has 2 writable mounts"},
		{"another file system at /dev/shm writable", []string{"93 27 254:0 /tmp /dev/shm rw - ext4 /dev/root rw"}, -1, "/dev/shm (ext4 254:0 /tmp) is writable"},
		{"a scratch directory mounted twice", []string{"99 90 254:0 /tmp/ec1/t /tmp/ec1/t rw - ext4 /dev/root rw"}, -1, "/tmp/ec1/t has 2 writable mounts"},
		{"a sibling of the checkout's directory", []string{"97 23 254:0 /home/u/src/other /mnt/other ro - ext4 /dev/root rw"}, -1, ""},
		{"another file system at the checkout's path", []string{"97 23 0:50 /home/u/src/m /mnt/x ro - ext4 /dev/vdb rw"}, -1, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var lines []string
			for i, l := range base {
				if i != c.drop {
					lines = append(lines, l)
				}
			}
			mounts, err := parseMountinfo(strings.Join(append(lines, c.extra...), "\n"))
			if err != nil {
				t.Fatal(err)
			}
			err = checkMounts(mounts, sb)
			switch {
			case c.want == "" && err != nil:
				t.Fatalf("refused a right table: %v", err)
			case c.want != "" && err == nil:
				t.Fatalf("accepted a table with %s", c.name)
			case c.want != "" && !strings.Contains(err.Error(), c.want):
				t.Fatalf("got %v, want it to say %q", err, c.want)
			}
		})
	}
}

// TestParseMountinfoUnescapes: a mount point with a space.
func TestParseMountinfoUnescapes(t *testing.T) {
	mounts, err := parseMountinfo(`7 1 0:5 /a\040b /mnt/a\040b ro,nosuid - tmpfs t rw` + "\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(mounts) != 1 || mounts[0].point != "/mnt/a b" || mounts[0].root != "/a b" || !mounts[0].readOnly {
		t.Fatalf("parsed %+v", mounts)
	}
}
