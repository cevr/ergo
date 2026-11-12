package eval

import (
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"testing"
	"time"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/runtime/rt"
	v1http "ergo.local/prototype/runtime/v1/http"
)

// TestHTTPPolicyAgreesWithRuntime holds std http's checks before the
// network (nativeshttp.go) to runtime/v1/http's, on generated input: a
// network policy's origins (schemes in any case, user information,
// default and zero-padded ports, IPv6 with zones, paths, queries,
// fragments) and limits; a URL fetched under a policy that allows no
// origin, so each one is refused or malformed before an exchange (control
// bytes, backslashes, a scheme with one slash, bad ports); and Listen of
// an address no host can bind. Both sides' outcomes are compared value for
// value and failure for failure. A failing seed prints the call and both
// outcomes, and ERGO_EVAL_HTTP_SEED=<seed> replays it.
//
// Mutants only it kills (testdata/mutants.json): oracle-o23-default-port,
// oracle-o23-host-case, oracle-o23-refused-allowed, oracle-o23-limits,
// oracle-o23-listen-text.
func TestHTTPPolicyAgreesWithRuntime(t *testing.T) {
	n := 300
	if testing.Short() {
		n = 100
	}
	first := uint64(0)
	if s := os.Getenv("ERGO_EVAL_HTTP_SEED"); s != "" {
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		first, n = v, 1
	}
	pkg := checked(t, "import http\n\ntest T {\n\tassert http.AnySuccess == http.AnySuccess\n}\n")
	funcs := map[string]*check.Func{}
	for _, p := range imports(pkg) {
		for _, f := range p.Funcs {
			funcs[p.Path+"."+f.Name] = f
		}
	}
	in := &interp{budget: DefaultBudget(), host: &host{}}
	fr := in.newFrame(pkg, pkg.Tests[0].Decl)
	call := func(name string, args ...Value) string {
		in.steps, in.bytes = 0, 0
		return outcome(func() (Value, *abrupt) { return natives[name](in, fr, fr.decl, funcs[name], args) })
	}
	fn := funcs["http.checkNetwork"]
	runtime := func(f func() any) string {
		switch e := f().(type) {
		case v1http.HTTPInvalidClient:
			return "fails " + describe(stdRecord(fn, "InvalidClient", map[string]Value{"Origin": e.Origin, "Reason": e.Reason}))
		case v1http.HTTPInvalidLimits:
			return "fails " + describe(stdRecord(fn, "InvalidLimits", map[string]Value{"MaxBodyBytes": int64(e.MaxBodyBytes), "MaxHeaderBytes": int64(e.MaxHeaderBytes), "IdleTimeout": int64(e.IdleTimeout), "ReadHeaderTimeout": int64(e.ReadHeaderTimeout), "ReadBodyTimeout": int64(e.ReadBodyTimeout)}))
		case v1http.HTTPOriginRefused:
			return "fails " + describe(stdRecord(fn, "OriginRefused", map[string]Value{"Origin": e.Origin}))
		case v1http.HTTPSendFailed:
			return "fails " + describe(stdRecord(fn, "SendFailed", map[string]Value{"Kind": int64(e.Kind), "Reason": e.Reason, "Limit": int64(e.Limit)}))
		case v1http.HTTPListenFailed:
			return "fails " + describe(stdRecord(fn, "ListenFailed", map[string]Value{"Addr": e.Addr, "Reason": e.Reason}))
		case []string:
			vs := make([]Value, len(e))
			for i, s := range e {
				vs[i] = s
			}
			return describe(newSlice(vs))
		case error:
			return "unexpected error " + e.Error()
		case nil:
			return "no outcome"
		}
		return "unexpected"
	}
	for seed := first; seed < first+uint64(n); seed++ {
		r := rand.New(rand.NewPCG(seed, 0x4778))
		pick := func(xs ...string) string { return xs[r.IntN(len(xs))] }
		host := func() string {
			return pick("example.com", "EXAMPLE.com", "Ex.Org", "127.0.0.1", "[::1]", "[0:0::1]", "[fe80::1%25eth0]", "[::ffff:1.2.3.4]", "[zz::1]", "a_b.test", "")
		}
		port := func() string {
			return pick("", "", ":80", ":443", ":8080", ":0080", ":0", ":65535", ":65536", ":x", ":")
		}
		origin := func() string {
			return pick("http", "https", "HTTP", "ftp", "") + pick("://", "://", ":/", "://user@") + host() + port() + pick("", "", "/", "/p", "?q", "#f", "?")
		}
		// checkNetwork: the canonical origins, or the first failure.
		var origins []string
		for range r.IntN(4) {
			origins = append(origins, origin())
		}
		lim := v1http.HTTPLimits{MaxBodyBytes: 1 + r.IntN(9), MaxHeaderBytes: 1 + r.IntN(9), IdleTimeout: time.Second, ReadHeaderTimeout: time.Second, ReadBodyTimeout: time.Second}
		switch r.IntN(8) {
		case 0:
			lim.MaxBodyBytes = -r.IntN(2)
		case 1:
			lim.ReadBodyTimeout = 0
		}
		limits := stdRecord(fn, "Limits", map[string]Value{"MaxBodyBytes": int64(lim.MaxBodyBytes), "MaxHeaderBytes": int64(lim.MaxHeaderBytes), "IdleTimeout": int64(lim.IdleTimeout), "ReadHeaderTimeout": int64(lim.ReadHeaderTimeout), "ReadBodyTimeout": int64(lim.ReadBodyTimeout)})
		texts := make([]Value, len(origins))
		for i, o := range origins {
			texts[i] = o
		}
		got := call("http.checkNetwork", newSlice(texts), limits)
		want := runtime(func() any {
			out, err := v1http.HTTPCheckNetwork(origins, lim)
			if err != nil {
				return err
			}
			return out
		})
		if got != want {
			t.Fatalf("seed %d: checkNetwork(%q, %+v): the evaluator gives %s, runtime/v1 %s\nreplay: ERGO_EVAL_HTTP_SEED=%d go test -run TestHTTPPolicyAgreesWithRuntime ./internal/sketch/eval", seed, origins, lim, got, want, seed)
		}

		// A fetch under a policy that allows no origin.
		raw := pick("", " ", "\t") + pick("http", "HTTPS", "ftp") + pick("://", ":\\\\", ":/", ":") + pick("", "u@") + host() + port() + pick("", "/a", "\\b", "?x", "#y", "/\n/c") + pick("", " ", "\x01")
		method := pick("GET", "POST", "G T", "")
		name := pick("Accept", "X-A", "Bad Name")
		value := pick("v", "a\r\nb")
		var owners []string
		var creds []v1http.HTTPField
		if r.IntN(3) == 0 {
			owners, creds = []string{origin()}, []v1http.HTTPField{{Name: "Authorization", Value: "x"}}
		}
		hops := int64(r.IntN(4) - 1)
		field := func(f v1http.HTTPField) Value {
			return stdRecord(fn, "Field", map[string]Value{"Name": f.Name, "Value": f.Value})
		}
		ownerTexts, credValues := []Value{}, []Value{}
		for i := range owners {
			ownerTexts, credValues = append(ownerTexts, owners[i]), append(credValues, field(creds[i]))
		}
		header := []v1http.HTTPField{{Name: name, Value: value}}
		got = call("http.fetch", newSlice(nil), false, limits, hops, method, raw, newSlice([]Value{field(header[0])}), newSlice(ownerTexts), newSlice(credValues), "")
		want = runtime(func() any {
			_, err := v1http.HTTPFetch(rt.Root("eval http policy"), nil, false, v1http.HTTPLimits{MaxBodyBytes: 1, MaxHeaderBytes: 1, IdleTimeout: 1, ReadHeaderTimeout: 1, ReadBodyTimeout: 1}, int(hops), method, raw, header, owners, creds, "")
			return err
		})
		if got != want {
			t.Fatalf("seed %d: fetch(%q %q, header %q, owners %q, hops %d): the evaluator gives %s, runtime/v1 %s\nreplay: ERGO_EVAL_HTTP_SEED=%d go test -run TestHTTPPolicyAgreesWithRuntime ./internal/sketch/eval", seed, method, raw, header, owners, hops, got, want, seed)
		}

		// Listen of an address no host binds: a port out of range or
		// not a number, or no port.
		addr := pick("127.0.0.1", "[::1]", "0.0.0.0") + pick(":99999", ":-1", ":x", "", ":65536", ":1a")
		got = call("http.Listen", addr)
		want = runtime(func() any {
			l, err := v1http.HTTPListen(addr)
			if err == nil {
				l.Close()
				return fmt.Errorf("bound %s", addr)
			}
			return err
		})
		if got != want {
			t.Fatalf("seed %d: Listen(%q): the evaluator gives %s, runtime/v1 %s\nreplay: ERGO_EVAL_HTTP_SEED=%d go test -run TestHTTPPolicyAgreesWithRuntime ./internal/sketch/eval", seed, addr, got, want, seed)
		}
	}
}
