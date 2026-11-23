package gocmd

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// Package is a package as a -gcflags pattern sees it.
type Package struct {
	ImportPath string
	Dir        string // absolute
}

// PackageFlags are the compiler flags the user's -gcflags give p, as
// cmd/go computes them (cmd/go/internal/load PerPackageFlag): each value
// is [pattern=]flags, a value without a pattern applies to the packages
// named on the command line, and the last value that matches p wins
// whole. cwd resolves relative patterns.
func (l Line) PackageFlags(p Package, cwd string) ([]string, error) {
	named := false
	for _, arg := range l.Packages {
		if strings.HasSuffix(arg, ".go") {
			continue
		}
		if MatchPackage(arg, cwd)(p) {
			named = true
		}
	}
	var flags []string
	for _, v := range l.GCFlags {
		match := func(Package) bool { return named }
		v = strings.TrimSpace(v)
		if v != "" && !strings.HasPrefix(v, "-") {
			i := strings.Index(v, "=")
			if i <= 0 {
				return nil, fmt.Errorf("-gcflags=%s: missing <pattern>=<value>", v)
			}
			match = MatchPackage(strings.TrimSpace(v[:i]), cwd)
			v = v[i+1:]
		}
		split, err := SplitQuoted(v)
		if err != nil {
			return nil, fmt.Errorf("-gcflags: %v", err)
		}
		if match(p) {
			flags = split
		}
	}
	return flags, nil
}

// MatchPackage is cmd/go's matcher of one package pattern
// (cmd/go/internal/load MatchPackage) for a package outside GOROOT: a
// relative pattern matches by folder, all every package, std and cmd no
// package of a module, and any other pattern by import path, where ...
// matches any string and a trailing /... also the path before it.
func MatchPackage(pattern, cwd string) func(Package) bool {
	switch {
	case pattern == "." || pattern == ".." || strings.HasPrefix(pattern, "./") || strings.HasPrefix(pattern, "../"):
		dir, rest := pattern, ""
		if i := strings.Index(pattern, "..."); i >= 0 {
			j := strings.LastIndex(pattern[:i], "/")
			dir, rest = pattern[:j], pattern[j+1:]
		}
		dir = filepath.Join(cwd, dir)
		if rest == "" {
			return func(p Package) bool { return p.Dir == dir }
		}
		match := matchPattern(rest)
		return func(p Package) bool {
			rel, err := filepath.Rel(dir, p.Dir)
			if err != nil {
				return false
			}
			rel = filepath.ToSlash(rel)
			return rel != ".." && !strings.HasPrefix(rel, "../") && match(rel)
		}
	case pattern == "all":
		return func(Package) bool { return true }
	case pattern == "std" || pattern == "cmd":
		return func(Package) bool { return false }
	}
	match := matchPattern(pattern)
	return func(p Package) bool { return match(p.ImportPath) }
}

// matchPattern is cmd/go's internal/pkgpattern MatchPattern: ... matches
// any string but never a vendor element, and x/... also matches x.
func matchPattern(pattern string) func(string) bool {
	const vendorChar = "\x00"
	if strings.Contains(pattern, vendorChar) || strings.Contains(pattern, `\`) {
		return func(string) bool { return false }
	}
	re := replaceVendor(regexp.QuoteMeta(pattern), vendorChar)
	switch {
	case strings.HasSuffix(re, `/`+vendorChar+`/\.\.\.`):
		re = strings.TrimSuffix(re, `/`+vendorChar+`/\.\.\.`) + `(/vendor|/` + vendorChar + `/\.\.\.)`
	case re == vendorChar+`/\.\.\.`:
		re = `(/vendor|/` + vendorChar + `/\.\.\.)`
	case strings.HasSuffix(re, `/\.\.\.`):
		re = strings.TrimSuffix(re, `/\.\.\.`) + `(/\.\.\.)?`
	}
	re = strings.ReplaceAll(re, `\.\.\.`, `[^`+vendorChar+`]*`)
	reg := regexp.MustCompile(`^` + re + `$`)
	return func(name string) bool {
		if strings.Contains(name, vendorChar) {
			return false
		}
		return reg.MatchString(replaceVendor(name, vendorChar))
	}
}

// replaceVendor replaces each path element "vendor" of x with repl.
func replaceVendor(x, repl string) string {
	if !strings.Contains(x, "vendor") {
		return x
	}
	elem := strings.Split(x, "/")
	for i := 0; i < len(elem)-1; i++ {
		if elem[i] == "vendor" {
			elem[i] = repl
		}
	}
	return strings.Join(elem, "/")
}

// SplitQuoted is cmd/internal/quoted Split: s split at spaces, where a
// field may be quoted with ' or " (no escapes inside).
func SplitQuoted(s string) ([]string, error) {
	var f []string
	for len(s) > 0 {
		for len(s) > 0 && isSpace(s[0]) {
			s = s[1:]
		}
		if len(s) == 0 {
			break
		}
		if s[0] == '"' || s[0] == '\'' {
			quote := s[0]
			s = s[1:]
			i := 0
			for i < len(s) && s[i] != quote {
				i++
			}
			if i >= len(s) {
				return nil, fmt.Errorf("unterminated %c string", quote)
			}
			f = append(f, s[:i])
			s = s[i+1:]
			continue
		}
		i := 0
		for i < len(s) && !isSpace(s[i]) {
			i++
		}
		f = append(f, s[:i])
		s = s[i:]
	}
	return f, nil
}

// JoinQuoted is cmd/internal/quoted Join: the fields that SplitQuoted
// reads back as args.
func JoinQuoted(args []string) (string, error) {
	var b strings.Builder
	for i, a := range args {
		if i > 0 {
			b.WriteByte(' ')
		}
		hasSpace, hasSingle, hasDouble := strings.ContainsAny(a, " \t\n\r"), strings.Contains(a, "'"), strings.Contains(a, `"`)
		switch {
		case a != "" && !hasSpace && !hasSingle && !hasDouble:
			b.WriteString(a)
		case !hasSingle:
			b.WriteString("'" + a + "'")
		case !hasDouble:
			b.WriteString(`"` + a + `"`)
		default:
			return "", fmt.Errorf("argument %q contains both quote characters", a)
		}
	}
	return b.String(), nil
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }
