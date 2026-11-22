package lower

import (
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// urlProgram reads one operation per line, its texts hex-encoded after an
// "x" (so a text may hold any byte, and "x" alone is ""), and prints one
// line per operation, every text in it quoted as Go's strconv.Quote does.
//
//	P t      Parse: "ok" and the parts, the text, Hostname, Port,
//	         EscapedPath and EscapedFragment; or "err" and Go's error text
//	R b r    Resolve r against b: the text, or "err" when either refuses
//	Q t      http.ParseQuery: each field's name and value
//	E t      QueryEscape, PathEscape, QueryUnescape and PathUnescape
const urlProgram = `import hex
import http
import io
import strconv
import strings
import url

func text(s string) string { hex.Decode(s[1:len(s)]) catch { case hex.Malformed: "?" } }

func q(s string) string { strconv.Quote(s) }

func parts(u url.URL) string {
	let user = match u.User {
	case Some[url.Userinfo] as x:
		q(x.Value.Username) + " " + q(x.Value.Password) + " \(x.Value.HasPassword)"
	case None:
		"nouser"
	}
	q(u.Scheme) + " " + q(u.Opaque) + " " + user + " " + q(u.Host) + " " + q(u.Path) + " " + q(u.RawPath) + " " + q(u.RawQuery) + " " + q(u.Fragment) + " " + q(u.RawFragment) + " \(u.ForceQuery) \(u.OmitHost) " + q(url.Text(u)) + " " + q(url.Hostname(u)) + " " + q(url.Port(u)) + " " + q(url.EscapedPath(u)) + " " + q(url.EscapedFragment(u))
}

func unescaped(s string, query bool) string {
	if query {
		let v = url.QueryUnescape(s) catch {
		case url.Malformed as m:
			return "err " + q(m.Reason)
		}
		return q(v)
	}
	let v = url.PathUnescape(s) catch {
	case url.Malformed as m:
		return "err " + q(m.Reason)
	}
	q(v)
}

func step(line string) string {
	let f = strings.Fields(line)
	let op = f[0]
	if op == "P" {
		let u = url.Parse(text(f[1])) catch {
		case url.Malformed as m:
			return "err " + q(url.MalformedText(m))
		}
		return "ok " + parts(u)
	}
	if op == "R" {
		let b = url.Parse(text(f[1])) catch {
		case url.Malformed:
			return "err"
		}
		let r = url.Parse(text(f[2])) catch {
		case url.Malformed:
			return "err"
		}
		return q(url.Text(url.Resolve(b, r)))
	}
	if op == "Q" {
		let mut out = "fields"
		for field in http.ParseQuery(text(f[1])) {
			out = out + " " + q(field.Name) + "=" + q(field.Value)
		}
		return out
	}
	let s = text(f[1])
	q(url.QueryEscape(s)) + " " + q(url.PathEscape(s)) + " " + unescaped(s, true) + " " + unescaped(s, false)
}

func main() fails io.ReadFailed needs Console | Ambient {
	let all = with context { io.Stdin: io.NewStdin() } { try io.Stdin.ReadAll() }
	for line in strings.Lines(all) {
		Console.Println(step(strings.TrimSuffix(line, "\n")))
	}
}
`

// urlRecorded are URLs recorded from Go's own tests of net/url
// (url_test.go: urltests, the ResolveReference and the String tables) and
// from what a browser sends a server: each is parsed, and resolved
// against each other's first, as the generated ones are.
var urlRecorded = []string{
	"http://www.google.com", "http://www.google.com/", "http://www.google.com/file%20one%26two",
	"http://www.google.com/#file%20one%26two", "ftp://webmaster@www.google.com/",
	"ftp://john%20doe@www.google.com/", "http://www.google.com/?", "http://www.google.com/?foo=bar?",
	"http://www.google.com/?q=go+language", "http://www.google.com/?q=go%20language",
	"http://www.google.com/a%20b?q=c+d", "http:www.google.com/?q=go+language",
	"http:%2f%2fwww.google.com/?q=go+language", "mailto:/webmaster@golang.org", "mailto:webmaster@golang.org",
	"/foo?query=http://bad", "//foo", "//user@foo/path?a=b", "///threeslashes",
	"http://user:password@google.com", "http://j@ne:password@google.com", "http://jane:p@ssword@google.com",
	"http://j@ne:password@google.com/p@th?q=@go", "http://www.google.com/?q=go+language#foo",
	"http://www.google.com/?q=go+language#foo&bar", "http://www.google.com/?q=go+language#foo%26bar",
	"file:///home/adg/rabbits", "file:///C:/FooBar/Baz.txt", "MaIlTo:webmaster@golang.org",
	"a/b/c", "http://%3Fam:pa%3Fsword@google.com", "http://192.168.0.1/", "http://192.168.0.1:8080/",
	"http://[fe80::1]/", "http://[fe80::1]:8080/", "http://[fe80::1%25en0]/", "http://[fe80::1%25en0]:8080/",
	"http://[fe80::1%25%65%6e%301-._~]/", "http://[fe80::1%25%65%6e%301-._~]:8080/",
	"http://rest.rsc.io/foo%2fbar/baz%2Fquux?alt=media", "mysql://a,b,c/bar", "scheme://!$&'()*+,;=hello!:1/path",
	"http://host/!$&'()*+,;=:@[hello]", "http://example.com/oid/[order_id]", "http://192.168.0.2:8080/foo",
	"http://192.168.0.2:/foo", "http://2b01:e34:ef40:7730:8e70:5aff:fefe:edac:8080/foo",
	"http://[2b01:e34:ef40:7730:8e70:5aff:fefe:edac]:8080/foo", "http://[2b01:e34:ef40:7730:8e70:5aff:fefe:edac]:/foo",
	"http://hello.世界.com/foo", "http://hello.%e4%b8%96%e7%95%8c.com/foo", "http://hello.%E4%B8%96%E7%95%8C.com/foo",
	"http://example.com//foo", "myscheme://authority<\"hi\">/foo", "tcp://[2020::2020:20:2020:2020%25Windows%20Loves%20Spaces]:2020",
	"magnet:?xt=urn:btih:c12fe1c06bba254a9dc9f519b335aa7c1367a88a&dn", "mailto:?subject=hi",
	"http://[::1]", "http://[::1]:80", "http://[1.2.3.4]/", "http://[::ffff:1.2.3.4]/", "http://a:1:2/",
	"x://a:1:2/", "http://[::1/", "http://x[::1]/", "http://a b/", "http://%41/", "http://%zz/", "http://a%2/",
	"http://foo.com/path%zz", "http://foo.com/#%zz", "%", "%zz", "a:b", ":", "://x", "http://x/%", "*",
	"http://example.com/a/b/c/../../g", "http://a/b/c/d;p?q", "g:h", "g", "./g", "g/", "/g", "//g", "?y", "g?y",
	"#s", "g#s", "g?y#s", ";x", "g;x", "g;x?y#s", "", ".", "./", "..", "../", "../g", "../..", "../../", "../../g",
	"../../../g", "../../../../g", "/./g", "/../g", "g.", ".g", "g..", "..g", "./../g", "./g/.", "g/./h", "g/../h",
	"g;x=1/./y", "g;x=1/../y", "g?y/./x", "g?y/../x", "g#s/./x", "g#s/../x", "http:g", "http://a/b/c/d;p?q#f",
	"https://h/%2F..%2F", "http:/opaque/../path", "mailto:x@y?z", "http://user@/p", "http://@/p", "http://:@h/",
	"http://u:p:q@h/", "http://[::1]:x/", "http://h:80x/", "http://h/a b", "http://h/?a b#c d", "http://h/\x7f",
	"http://h/\x00", "http://h/\t", "http://h/\u00e9", "\xff://x", "a\xff:b",
}

// urlPieces are the pieces generated URLs are made of: schemes,
// authorities, paths, queries and fragments, each well formed or not.
var urlPieces = [5][]string{
	{"", "", "http:", "https:", "HTTP:", "ftp:", "mailto:", "a+b.c-d:", "1x:", ":", "file:", "x:"},
	{"", "", "//h", "//example.com", "//h:80", "//h:", "//h:8x", "//[::1]", "//[::1]:443", "//[fe80::1%25en0]",
		"//[1.2.3.4]", "//[::ffff:1.2.3.4]", "//a:1:2", "//%41b", "//a%2", "//[", "//x]", "//a b", "//u@h", "//u:p@h",
		"//u%20v:p%3A@h", "//@h", "//u@@h", "//u v@h", "//h\u00e9", "///", "//[::1%25]", "//[12345::]", "//[1::2::3]",
		"//[1.2.3]", "//[::1.2.3.04]", "//[1:2:3:4:5:6:7:8:9]", "//[::g]", "//[1:2:3:4:5:6:1.2.3.4]"},
	{"", "/", "/a/b", "/a%20b", "/a b", "/a%2Fb", "/a/./b/../c", "/../..", "a/b", "a:b/c", "./a:b", "/%", "/%zz",
		"/a+b", "/\u00e9", "/a;b=c", "/[x]", "//double", "/a/..", "/a/.", "/*", "*", "/a%252F"},
	{"", "", "?", "?a=1", "?a=1&b=2&a=3", "?q=go+language", "?a%20b=c%2Bd", "?a;b", "?%zz=1&ok=2", "?=x", "?x=",
		"?&&", "??", "?a=b?c", "?\u00e9=\u00e8"},
	{"", "", "#", "#f", "#a%20b", "#a b", "#%zz", "#x#y", "#/a?b", "#\u00e9"},
}

// urlAlphabet are the bytes a mutation writes into a generated URL.
const urlAlphabet = "%/?#:@[]. +&=;-_~!$'()*,aAzZ09\x00\x1f\x7f\x80\xe9\xff\t"

// urlCase is one generated URL: pieces, then up to two byte mutations.
func urlCase(r *rand.Rand) string {
	if r.IntN(4) == 0 {
		return urlRecorded[r.IntN(len(urlRecorded))]
	}
	var b strings.Builder
	for _, ps := range urlPieces {
		b.WriteString(ps[r.IntN(len(ps))])
	}
	s := []byte(b.String())
	for n := r.IntN(3); n > 0 && len(s) > 0; n-- {
		i := r.IntN(len(s))
		switch r.IntN(3) {
		case 0:
			s[i] = urlAlphabet[r.IntN(len(urlAlphabet))]
		case 1:
			s = append(s[:i], s[i+1:]...)
		default:
			s = append(s[:i], append([]byte{urlAlphabet[r.IntN(len(urlAlphabet))]}, s[i:]...)...)
		}
	}
	return string(s)
}

// urlHex is s as the program reads a text.
func urlHex(s string) string { return "x" + hex.EncodeToString([]byte(s)) }

// urlParts is the model of the program's P line: Go's url.Parse.
func urlParts(s string) string {
	u, err := url.Parse(s)
	if err != nil {
		return "err " + strconv.Quote(err.Error())
	}
	q := strconv.Quote
	user := "nouser"
	if u.User != nil {
		p, set := u.User.Password()
		user = fmt.Sprintf("%s %s %v", q(u.User.Username()), q(p), set)
	}
	return fmt.Sprintf("ok %s %s %s %s %s %s %s %s %s %v %v %s %s %s %s %s", q(u.Scheme), q(u.Opaque), user, q(u.Host), q(u.Path),
		q(u.RawPath), q(u.RawQuery), q(u.Fragment), q(u.RawFragment), u.ForceQuery, u.OmitHost, q(u.String()),
		q(u.Hostname()), q(u.Port()), q(u.EscapedPath()), q(u.EscapedFragment()))
}

// urlFields is the model of the program's Q line: Go's url.ParseQuery,
// its keys sorted and each key's values in order, as std http gives them.
func urlFields(raw string) string {
	values, _ := url.ParseQuery(raw)
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := "fields"
	for _, k := range keys {
		for _, v := range values[k] {
			out += " " + strconv.Quote(k) + "=" + strconv.Quote(v)
		}
	}
	return out
}

// urlEscapes is the model of the program's E line.
func urlEscapes(s string) string {
	un := func(v string, err error) string {
		if err != nil {
			return "err " + strconv.Quote(err.Error())
		}
		return strconv.Quote(v)
	}
	return strconv.Quote(url.QueryEscape(s)) + " " + strconv.Quote(url.PathEscape(s)) + " " + un(url.QueryUnescape(s)) + " " + un(url.PathUnescape(s))
}

// urlModelCases are n seeded operations and the model's line for each.
func urlModelCases(r *rand.Rand, n int) (cases, want []string) {
	add := func(c, w string) { cases, want = append(cases, c), append(want, w) }
	for _, s := range urlRecorded {
		add("P "+urlHex(s), urlParts(s))
	}
	// Go refuses a query of more than 10000 pairs, and reads one of 10000.
	for _, n := range []int{10000, 10001} {
		raw := strings.Repeat("a=1&", n-1) + "b=2"
		add("Q "+urlHex(raw), urlFields(raw))
	}
	for range n {
		s := urlCase(r)
		switch r.IntN(4) {
		case 0:
			add("P "+urlHex(s), urlParts(s))
		case 1:
			ref := urlCase(r)
			b, err1 := url.Parse(s)
			x, err2 := url.Parse(ref)
			w := "err"
			if err1 == nil && err2 == nil {
				w = strconv.Quote(b.ResolveReference(x).String())
			}
			add("R "+urlHex(s)+" "+urlHex(ref), w)
		case 2:
			_, raw, _ := strings.Cut(s, "?")
			add("Q "+urlHex(raw), urlFields(raw))
		default:
			add("E "+urlHex(s), urlEscapes(s))
		}
	}
	return cases, want
}

// TestStdURLMatchesGo (STD-SURVEY SM2; stdlib.md, url): std url's Parse,
// Text, Resolve, Hostname, Port, escapes and http.ParseQuery against Go's
// net/url, on the recorded URLs of urlRecorded and 20,000 seeded ones
// built from urlPieces and mutated a byte at a time, so each refusal Go
// words (a bad escape, a host character, a port, an IP literal and
// netip's reasons, a colon in the first segment, a control byte, a
// userinfo) is met. Every case compares every part, the text and the
// error's words. The seed is 1, or ERGO_SEED; a failing line prints the
// seed, the operation, Ergo's line and Go's.
//
// It builds Go because it compares the runs of a built program with Go's
// own package, and url is Ergo source that no in-process interpreter runs
// (test-speed T8); one build serves every case.
func TestStdURLMatchesGo(t *testing.T) {
	needsBuild(t)
	seed := uint64(1)
	if s := os.Getenv("ERGO_SEED"); s != "" {
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatalf("ERGO_SEED=%q: %v", s, err)
		}
		seed = v
	}
	cases, want := urlModelCases(rand.New(rand.NewPCG(seed, 7)), 20000)
	exes := tableExes(t, "url")
	if exes == nil {
		t.Fatal("the lowered program does not build")
	}
	stdout, stderr, code := runExeInput(t, exes["url"], strings.Join(cases, "\n")+"\n")
	if code != 0 {
		t.Fatalf("seed %d: exit %d\n%s", seed, code, firstLines(stderr, 20))
	}
	got := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if len(got) != len(cases) {
		t.Fatalf("seed %d: %d lines for %d cases", seed, len(got), len(cases))
	}
	bad := 0
	for i := range cases {
		if got[i] != want[i] {
			t.Errorf("seed %d: %s\n  Ergo %s\n  Go   %s", seed, urlCaseText(cases[i]), got[i], want[i])
			if bad++; bad == 10 {
				t.FailNow()
			}
		}
	}
}

// urlCaseText is a case line with its texts decoded and quoted.
func urlCaseText(line string) string {
	f := strings.Fields(line)
	for i := 1; i < len(f); i++ {
		b, _ := hex.DecodeString(f[i][1:])
		f[i] = strconv.Quote(string(b))
	}
	return strings.Join(f, " ")
}
