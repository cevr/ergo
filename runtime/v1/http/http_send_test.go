package http

import (
	"io"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/runtime/rt"
)

// TestHTTPSendWritesTheText sends seeded JSON texts through a loopback
// server with Exchange.Send (std http SendBytes, GX9): the client gets the
// text as the whole body, with its length declared, never chunked, for a
// GET and for a HEAD (which gets the length and no body); the response
// the handler returns after Send is not sent; a Write after Send fails.
// The Recorder's law (SuccessJSONIsSuccess) cannot see a socket's
// framing or a second answer.
//
// Mutants it kills (testdata/mutants.json): gx9-send-no-length,
// gx9-send-unended. (Send leaving started unset is equivalent: Serve's
// second answer finds the header written and the declared length spent.)
func TestHTTPSendWritesTheText(t *testing.T) {
	texts := make(chan string, 1)
	l, _ := startHTTP(t, func(ct *rt.Task, x *Exchange, _, _, _ string, _, _ []HTTPField, _ string, _ bool, _ string) HTTPResponse {
		text := <-texts
		x.Send(200, []HTTPField{{"Content-Type", "application/json"}}, []byte(text))
		if err := x.Write(ct, "more"); err != (HTTPWriteFailed{Reason: "the stream has ended"}) {
			t.Errorf("write after Send: %v", err)
		}
		return HTTPResponse{Status: 500, Body: "not sent"}
	})
	client := httpClient(t)
	for seed := uint64(1); seed <= 8; seed++ {
		rng := rand.New(rand.NewPCG(seed, seed+17))
		// Past net/http's 2048-byte buffer a body with no declared
		// length is chunked, so the long texts see the length.
		n := []int{0, 1 + rng.IntN(100), 3000 + rng.IntN(6000)}[rng.IntN(3)]
		text := strings.Repeat(string(rune('a'+rng.IntN(26))), n)
		for _, method := range []string{"GET", "HEAD"} {
			texts <- text
			resp := httpRequest(t, client, method, "http://"+l.Addr()+"/send", "")
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			want := text
			if method == "HEAD" {
				want = ""
			}
			if resp.StatusCode != 200 || string(body) != want || resp.Header.Get("Content-Length") != strconv.Itoa(n) || len(resp.TransferEncoding) != 0 {
				t.Fatalf("seed %d %s of %d bytes: %d, length %q, encoding %v, body %d bytes %q...", seed, method, n, resp.StatusCode, resp.Header.Get("Content-Length"), resp.TransferEncoding, len(body), string(body[:min(len(body), 20)]))
			}
		}
	}
}
