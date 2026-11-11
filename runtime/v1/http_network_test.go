package v1

import (
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"ergo.local/prototype/internal/testwait"
	"ergo.local/prototype/runtime/rt"
)

type networkNode struct {
	host, status int
	next         int
}
type networkArrival struct {
	host, index                int
	method, body, a, b, public string
}

// TestHTTPNetworkMatchesModel kills CU5's policy, credential, hop and
// NoRedirects mutants, none of which fixed-origin HTTPDo can reach. Two
// loopback origins share a hostname and differ by port. Generated chains
// include relative and absolute redirects, A-B-A returns, terminal answers
// and cycles; an independent graph walk predicts every exchange and failure.
func TestHTTPNetworkMatchesModel(t *testing.T) {
	var mu sync.Mutex
	var nodes []networkNode
	var arrived []networkArrival
	var origins [2]string
	handler := func(host int) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			index, err := strconv.Atoi(r.URL.Path[1:])
			if err != nil {
				t.Error(err)
				return
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			arrived = append(arrived, networkArrival{host, index, r.Method, string(body), strings.Join(r.Header.Values("X-Mark-A"), ","), strings.Join(r.Header.Values("X-Mark-B"), ","), strings.Join(r.Header.Values("X-Public"), ",")})
			n := nodes[index]
			if n.next >= 0 {
				to := nodes[n.next]
				location := fmt.Sprintf("/%d", n.next)
				if to.host != host || index%2 == 0 {
					location = origins[to.host] + location
				}
				w.Header().Set("Location", location)
			}
			w.WriteHeader(n.status)
			_, _ = w.Write([]byte("answer"))
		}
	}
	a, b := httptest.NewServer(handler(0)), httptest.NewServer(handler(1))
	defer a.Close()
	defer b.Close()
	origins = [2]string{a.URL, b.URL}
	seeds := make([]uint64, 64)
	for i := range seeds {
		seeds[i] = uint64(i + 1)
	}
	if s := os.Getenv("ERGO_HTTP_NETWORK_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = []uint64{n}
	}
	for _, seed := range seeds {
		r := rand.New(rand.NewPCG(seed, 23))
		// Pure generated identities reach normalization that random loopback
		// ports cannot: default ports and case-sensitive IPv6 interface zones.
		defaultPort := []int{80, 443}[r.IntN(2)]
		scheme := "http"
		if defaultPort == 443 {
			scheme = "https"
		}
		name := fmt.Sprintf("ExAmPle-%d.TeSt", r.IntN(100))
		canonical, err := HTTPCheckNetwork([]string{fmt.Sprintf("%s://%s:00%d", scheme, name, defaultPort)}, serveLimits(128, 8192))
		if err != nil || len(canonical) != 1 || canonical[0] != scheme+"://"+strings.ToLower(name) {
			t.Fatalf("seed %d: default-port origin %q, %v", seed, canonical, err)
		}
		zone := fmt.Sprintf("En%d", r.IntN(20))
		canonical, err = HTTPCheckNetwork([]string{scheme + "://[FE80:0:0:0:0:0:0:1%25" + zone + "]:" + strconv.Itoa(defaultPort)}, serveLimits(128, 8192))
		if err != nil || len(canonical) != 1 || canonical[0] != scheme+"://[fe80::1%25"+zone+"]" {
			t.Fatalf("seed %d: IPv6-zone origin %q, %v", seed, canonical, err)
		}
		roundTrip, err := HTTPCheckNetwork(canonical, serveLimits(128, 8192))
		if err != nil || !slices.Equal(roundTrip, canonical) {
			t.Fatalf("seed %d: origin identity is not stable: %q -> %q, %v", seed, canonical, roundTrip, err)
		}
		graph := make([]networkNode, 3+r.IntN(5))
		statuses := []int{301, 302, 303, 307, 308}
		for i := range graph {
			graph[i] = networkNode{r.IntN(2), statuses[r.IntN(len(statuses))], i + 1}
		}
		graph[0].host = 0
		graph[1].host, graph[2].host = 1, 0
		last := len(graph) - 1
		if r.IntN(2) == 0 {
			graph[last].status, graph[last].next = 200, -1
		} else {
			graph[last].next = r.IntN(len(graph))
		}
		hops := r.IntN(10) - 1
		mode := r.IntN(4)
		allowed := []string{}
		if mode == 1 {
			allowed = []string{origins[0]}
		}
		if mode == 2 {
			allowed = []string{origins[0], origins[1]}
		}
		method := []string{"GET", "HEAD", "POST"}[r.IntN(3)]
		body := "payload"
		var owners []string
		var credentials, header []HTTPField
		var marks [2][]string
		for host := range origins {
			for range r.IntN(4) {
				value := fmt.Sprintf("mark-%d", r.IntN(100))
				marks[host] = append(marks[host], value)
				owners = append(owners, origins[host])
				credentials = append(credentials, HTTPField{[]string{"X-Mark-A", "X-Mark-B"}[host], value})
			}
		}
		var public []string
		for range r.IntN(4) {
			value := fmt.Sprintf("visible-%d", r.IntN(100))
			public = append(public, value)
			header = append(header, HTTPField{"X-Public", value})
		}
		mu.Lock()
		nodes, arrived = graph, nil
		mu.Unlock()
		res, err := HTTPFetch(rt.Root("network model"), allowed, mode == 0, serveLimits(128, 8192), hops, method, origins[0]+"/0", header, owners, credentials, body)
		var want []networkArrival
		at, followed := 0, 0
		wantFailure := ""
		wantOrigin := ""
		wantStatus := 0
		wantBody := "answer"
		for {
			n := graph[at]
			// This is a graph/policy model, not networkOrigin or originAllowed.
			if mode == 3 || mode == 1 && n.host != 0 {
				wantFailure, wantOrigin = "origin", origins[n.host]
				break
			}
			markA, markB := "", ""
			if n.host == 0 {
				markA = strings.Join(marks[0], ",")
			} else {
				markB = strings.Join(marks[1], ",")
			}
			want = append(want, networkArrival{n.host, at, method, body, markA, markB, strings.Join(public, ",")})
			if n.next < 0 || hops == -1 {
				wantStatus = n.status
				if method == "HEAD" {
					wantBody = ""
				}
				break
			}
			if followed == hops {
				wantFailure = "hops"
				break
			}
			if n.status == 303 && method != "HEAD" || (n.status == 301 || n.status == 302) && method != "GET" && method != "HEAD" {
				method, body = "GET", ""
			}
			at = n.next
			followed++
		}
		mu.Lock()
		got := slices.Clone(arrived)
		mu.Unlock()
		fail := func(reason string) {
			t.Fatalf("seed %d: %s\ngraph=%+v mode=%d hops=%d\nwant=%+v\ngot=%+v\nresponse=%+v error=%v\nreplay: ERGO_HTTP_NETWORK_SEED=%d go test -run '^TestHTTPNetworkMatchesModel$' ./runtime/v1", seed, reason, graph, mode, hops, want, got, res, err, seed)
		}
		if !slices.Equal(got, want) {
			fail("the observed exchanges differ from the graph model")
		}
		switch wantFailure {
		case "origin":
			var e HTTPOriginRefused
			if !errors.As(err, &e) || e.Origin != wantOrigin {
				fail("wrong refused origin")
			}
		case "hops":
			var e HTTPRedirectLimit
			if !errors.As(err, &e) || e.Limit != hops {
				fail("wrong hop refusal")
			}
		default:
			if err != nil || res.Status != wantStatus || res.Body != wantBody {
				fail("wrong final response")
			}
		}
	}
}

// TestHTTPNetworkStops kills cu5-caller-lost: fixed-origin cancellation
// cannot detect a network traversal that discards the task after a redirect.
func TestHTTPNetworkStops(t *testing.T) {
	entered, disconnected, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-r.Context().Done():
			close(disconnected)
		case <-release:
		}
	}))
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", peer.URL+"/held")
		w.WriteHeader(307)
	}))
	t.Cleanup(func() { close(release); first.Close(); peer.Close() })
	caller := rt.Root("stopped network caller")
	ended := make(chan any, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				ended <- p
			}
		}()
		_, err := HTTPFetch(caller, nil, true, serveLimits(128, 8192), 1, "GET", first.URL+"/", nil, nil, nil, "")
		ended <- err
	}()
	select {
	case <-entered:
	case <-testwait.After(t):
		t.Fatal("redirect target was not reached")
	}
	caller.Interrupt()
	select {
	case p := <-ended:
		if p != rt.Interrupted {
			t.Fatalf("call ended %v, want interruption", p)
		}
	case <-testwait.After(t):
		t.Fatal("network stop did not join its exchange")
	}
	select {
	case <-disconnected:
	case <-testwait.After(t):
		t.Fatal("redirect target did not see the hang-up")
	}
}
