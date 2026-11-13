package eval

import (
	"path"
	"strings"

	"ergo.local/prototype/internal/sketch/syntax"
)

// Symbolic links of the simulated file system (ORACLE O28). A link is a
// hostFile with a target. The host's reads (fs.Host's ReadFile, ReadDir,
// Stat and OpenRoot) follow links as Linux path resolution does, element
// by element, ".." from the directory reached, at most 40 links. An
// opened fs.HostRoot resolves as Go's os.Root does (os/root_openat.go,
// doInRoot): ".." removes the element before it, an absolute target or a
// step above the root is Escapes, a link's target is spliced in where
// the link was, and at most 8 links are followed. A write, a removal, a
// rename, a chmod or a mkdir whose path reaches a link is a counted skip.

// maxLinks is Linux's limit on the links one resolution follows (ELOOP).
const maxLinks = 40

// resolve is the entry at given after every link, Linux's way: the path
// of that entry and the entry, or why it is not one (NotFound, or a
// Failed reason).
func (h *host) resolve(given string) (string, *hostFile, string, string) {
	p := given
	if !strings.HasPrefix(p, "/") {
		p = h.cwd + "/" + p
	}
	todo := strings.Split(p, "/")
	at, links := "/", 0
	for len(todo) > 0 {
		part := todo[0]
		todo = todo[1:]
		switch part {
		case "", ".":
			continue
		case "..":
			at = path.Dir(at)
			continue
		}
		dir := h.files[at]
		if dir == nil || !dir.dir {
			return "", nil, "Failed", "not a directory"
		}
		next := path.Join(at, part)
		f := h.files[next]
		switch {
		case f == nil:
			return "", nil, "NotFound", ""
		case f.link != "":
			links++
			if links > maxLinks {
				return "", nil, "Failed", "too many levels of symbolic links"
			}
			target := f.link
			if strings.HasPrefix(target, "/") {
				at = "/"
			}
			todo = append(strings.Split(target, "/"), todo...)
			continue
		}
		at = next
	}
	return at, h.files[at], "", ""
}

// linked reports whether resolving p (an absolute path) meets a link.
func (h *host) linked(p string) bool {
	for d := p; ; d = path.Dir(d) {
		if f := h.files[d]; f != nil && f.link != "" {
			return true
		}
		if d == "/" {
			return false
		}
	}
}

// rootResolve is the entry at name below the root directory dir, as Go's
// os.Root resolves it: its path and entry, or the failure: Escapes, or
// NotFound, or a Failed reason. A final element that ends in a slash must
// be a directory.
func (h *host) rootResolve(dir, name string) (string, *hostFile, string, string) {
	parts, slash, kind := rootSplit(name, nil, nil)
	if kind != "" {
		return "", nil, kind, "empty path"
	}
	links := 0
	for i := 0; i < len(parts); {
		if parts[i] == ".." {
			end := i + 1
			for end < len(parts) && parts[end] == ".." {
				end++
			}
			n := end - i
			if n > i {
				return "", nil, "Escapes", ""
			}
			parts = append(parts[:i-n:i-n], parts[end:]...)
			if len(parts) == 0 {
				parts = []string{"."}
			}
			i = 0
			continue
		}
		p := path.Join(append([]string{dir}, parts[:i+1]...)...)
		f := h.files[p]
		if f == nil {
			return "", nil, "NotFound", ""
		}
		if f.link != "" {
			links++
			if links > 8 {
				return "", nil, "Failed", "too many levels of symbolic links"
			}
			last := i == len(parts)-1
			next, nextSlash, kind := rootSplit(f.link, parts[:i], parts[i+1:])
			if kind != "" {
				return "", nil, kind, "empty path"
			}
			if last && nextSlash {
				slash = true
			}
			parts, i = next, 0
			continue
		}
		if i < len(parts)-1 && !f.dir || i == len(parts)-1 && slash && !f.dir {
			return "", nil, "Failed", "not a directory"
		}
		if i == len(parts)-1 {
			return p, f, "", ""
		}
		i++
	}
	return dir, h.files[dir], "", ""
}

// rootSplit is os's splitPathInRoot: s's elements between prefix and
// suffix, "." elements dropped but at the end, and whether s ends in a
// slash; kind is Escapes for an absolute s, Failed for an empty one.
func rootSplit(s string, prefix, suffix []string) (parts []string, slash bool, kind string) {
	if s == "" {
		return nil, false, "Failed"
	}
	if s[0] == '/' {
		return nil, false, "Escapes"
	}
	parts = append([]string{}, prefix...)
	elems := strings.Split(s, "/")
	for len(elems) > 1 && elems[len(elems)-1] == "" {
		elems, slash = elems[:len(elems)-1], true
	}
	for k, e := range elems {
		if e == "" {
			continue
		}
		parts = append(parts, e)
		if e == "." && k < len(elems)-1 {
			parts = parts[:len(parts)-1]
		}
	}
	if len(suffix) > 0 && len(parts) > 0 && parts[len(parts)-1] == "." {
		parts = parts[:len(parts)-1]
	}
	return append(parts, suffix...), slash, ""
}

// rootRead is one read of an opened HostRoot: ReadFile, ReadDir or Stat.
func (r *hostRootV) read(in *interp, fr *frame, at syntax.Node, name, given string) (Value, *abrupt) {
	h := in.host
	failed := func(kind, reason string) *abrupt {
		fields := map[string]Value{"Path": given}
		if kind == "Failed" {
			fields["Reason"] = reason
		}
		return fr.abrupt(ctlFail, at, stdRecord(r.fn, kind, fields), "")
	}
	p, f, kind, reason := h.rootResolve(r.dir, given)
	if kind != "" {
		return nil, failed(kind, reason)
	}
	switch name {
	case "ReadFile":
		if f.dir {
			return nil, failed("Failed", "is a directory")
		}
		if ab := in.build(fr, at, 1, 0, int64(len(f.data))); ab != nil {
			return nil, ab
		}
		return f.data, nil
	case "ReadDir":
		if !f.dir {
			return nil, failed("Failed", "not a directory")
		}
		return stringSlice(h.children(p)), nil
	}
	if f.dir {
		return nil, fr.unsupported(at, "the size of a directory, which the host's file system decides")
	}
	return stdRecord(r.fn, "Info", map[string]Value{"Name": path.Base(given), "Size": int64(len(f.data)), "Dir": false, "Perm": int64(f.perm)}), nil
}
