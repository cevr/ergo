package closure

import (
	"fmt"
	"path"
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
		out = append(out, mount{
			id:       id,
			dev:      fields[2],
			root:     unescapeMountinfo(fields[3]),
			point:    unescapeMountinfo(fields[4]),
			readOnly: options[0] == "ro",
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

// checkMounts is the rule of the sandbox's mount table:
//
//   - S1: every mount is read-only, except one writable mount at each
//     scratch directory;
//   - S2: each scratch directory is such a mount;
//   - no other view of the checkout: no mount lies inside the checkout, and
//     no mount other than the one that holds it shows the checkout's
//     directory of its file system or one above it (a bind alias), so the
//     checkout's path is the only name of each of its directories.
func checkMounts(mounts []mount, sb sandbox) error {
	writable := map[string]int{}
	var holder *mount
	for i, m := range mounts {
		if !m.readOnly {
			if !scratchDir(sb, m.point) {
				return fmt.Errorf("the mount at %s (%s %s) is writable", m.point, m.dev, m.root)
			}
			writable[m.point]++
		}
		if below(sb.Root, m.point) && (holder == nil || len(m.point) >= len(holder.point)) {
			holder = &mounts[i]
		}
	}
	for _, dir := range sb.Scratch {
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
	sb := sandbox{Root: "/home/u/src/m", Scratch: []string{"/tmp/ec1/t", "/tmp/ec1/c"}}
	base := []string{
		"23 1 254:0 / / ro,relatime shared:1 - ext4 /dev/root rw",
		"25 23 0:22 / /proc ro,relatime - proc proc rw",
		"40 23 0:33 / /workspaces ro,noatime - btrfs /dev/loop0 rw",
		"90 23 254:0 /tmp/ec1/t /tmp/ec1/t rw,relatime - ext4 /dev/root rw",
		"91 23 254:0 /tmp/ec1/c /tmp/ec1/c rw,relatime - ext4 /dev/root rw",
	}
	cases := []struct {
		name  string
		extra []string
		drop  int // index of a base line to leave out, or -1
		want  string
	}{
		{"sandbox", nil, -1, ""},
		{"a writable mount", []string{"95 23 0:40 / /data rw - tmpfs tmpfs rw"}, -1, "/data (0:40 /) is writable"},
		{"the root file system writable", []string{"96 1 254:0 / / rw - ext4 /dev/root rw"}, 0, "the mount at / (254:0 /) is writable"},
		{"a writable bind alias of the checkout", []string{"97 23 254:0 /home/u/src/m /tmp/alias rw - ext4 /dev/root rw"}, -1, "/tmp/alias (254:0 /home/u/src/m) is writable"},
		{"a read-only bind alias of the checkout", []string{"97 23 254:0 /home/u/src/m /tmp/alias ro - ext4 /dev/root rw"}, -1, "shows the checkout again"},
		{"a bind alias of a directory in the checkout", []string{"97 23 254:0 /home/u/src/m/docs /tmp/docs ro - ext4 /dev/root rw"}, -1, "shows the checkout again"},
		{"a bind alias of an ancestor", []string{"97 23 254:0 /home /mnt/home ro - ext4 /dev/root rw"}, -1, "shows the checkout again"},
		{"a second mount of the file system", []string{"97 23 254:0 / /mnt/root ro - ext4 /dev/root rw"}, -1, "shows the checkout again"},
		{"a mount inside the checkout", []string{"98 23 0:41 / /home/u/src/m/cache ro - tmpfs tmpfs rw"}, -1, "inside the checkout at /home/u/src/m/cache"},
		{"a scratch directory not mounted", nil, 4, "/tmp/ec1/c has 0 writable mounts"},
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
