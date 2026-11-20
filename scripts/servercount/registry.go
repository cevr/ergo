package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
)

// The registry's model (benchmarks/server/registry, api.ergo): what the
// program, its control and its floor must answer. It is written again
// here, from the routes' contract, not from any of the three, so a side
// that drifts is a wrong answer. Its constants are the program's.
const (
	packageCount = 3000
	maxBody      = 1 << 12
	hookKey      = "mirror-hook-key"
)

// packageName is the name of the i-th package (catalog.NameOf).
func packageName(i int) string { return fmt.Sprintf("pkg%04d", i) }

// listingBody is the body of GET /packages.
var listingBody = func() string {
	var b strings.Builder
	b.WriteString(`{"packages":[`)
	for i := range packageCount {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"name":"%s","version":"%d.%d.%d","summary":"package %s of the mirror, kept for its clients","size":%d}`, packageName(i), i%7, i%13, i%5, packageName(i), i*7919%100000)
	}
	b.WriteString("]}")
	return b.String()
}()

// problem is the body of a refusal.
func problem(message string) string { return `{"error":"` + message + `"}` }

// sign is a hook body's signature: hex HMAC-SHA-256 under hookKey.
func sign(body string) string {
	m := hmac.New(sha256.New, []byte(hookKey))
	m.Write([]byte(body))
	return hex.EncodeToString(m.Sum(nil))
}

// model is the registry's state: each client's pins, as their JSON
// texts, and their count.
type model struct {
	pins  map[string][]string
	total int
}

func newModel() *model { return &model{pins: map[string][]string{}} }

// The requests of a script, each with the model's answer.

func (m *model) listing(conn int) Exchange {
	return expect(conn, "GET", "/packages", nil, "", 200, listingBody)
}

// pin asks for package k at a version for client; the model keeps it.
func (m *model) pin(conn int, client string, k int, version string) Exchange {
	body := `{"client":"` + client + `","name":"` + packageName(k) + `","version":"` + version + `"}`
	m.pins[client] = append(m.pins[client], `{"name":"`+packageName(k)+`","version":"`+version+`"}`)
	m.total++
	return expect(conn, "POST", "/pins", nil, body, 201, `{"client":"`+client+`","pins":`+strconv.Itoa(len(m.pins[client]))+`}`)
}

// refusedPin is a POST /pins of body, refused with status and message.
func (m *model) refusedPin(conn int, body string, status int, message string) Exchange {
	return expect(conn, "POST", "/pins", nil, body, status, problem(message))
}

func (m *model) show(conn int, client string) Exchange {
	return expect(conn, "GET", "/pins/"+client, nil, "", 200, `{"client":"`+client+`","pins":[`+strings.Join(m.pins[client], ",")+`]}`)
}

func (m *model) status(conn int) Exchange {
	return expect(conn, "GET", "/status", nil, "", 200, `{"status":"ok","pins":`+strconv.Itoa(m.total)+`}`)
}

// hook is a POST /hooks of event n, signed or not.
func (m *model) hook(conn int, n int, signed bool) Exchange {
	body := `{"event":"sync","n":` + strconv.Itoa(n) + `}`
	signature := sign(body)
	if !signed {
		signature = strings.Repeat("0", len(signature))
	}
	if signed {
		return expect(conn, "POST", "/hooks", [][2]string{{"X-Signature", signature}}, body, 204, "")
	}
	return expect(conn, "POST", "/hooks", [][2]string{{"X-Signature", signature}}, body, 401, problem("bad signature"))
}

func (m *model) noRoute(conn int, method, path string) Exchange {
	return expect(conn, method, path, nil, "", 404, problem("no route"))
}

// Kinds are the route kinds a receipt counts, each a script of one kind
// of request.
var Kinds = []string{"listing", "pin", "show", "status", "hook"}

// sizes are a kind's two script lengths: the receipt's per-request values
// are slopes between them, so start-up, setup and the exit cancel.
func sizes(kind string) (small, large int) {
	if kind == "listing" {
		return 50, 450
	}
	return 1000, 6000
}

// clientsPerConn is how many clients each connection pins for: a client
// is only ever sent on its own connection, so its answers do not depend
// on the order of connections.
const clientsPerConn = 16

// clientOf is the i-th client of connection conn.
func clientOf(conn, i int) string {
	return "c" + strconv.Itoa(conn) + "-" + strconv.Itoa(i%clientsPerConn)
}

// KindScript is n requests of kind over conns connections. show and
// status have a setup: four pins of each client, and nothing pins after
// it, so the total a status answers is the same on every connection.
func KindScript(kind string, n, conns int) Script {
	m := newModel()
	s := Script{Conns: conns}
	if kind == "show" || kind == "status" {
		for c := range conns {
			for i := range 4 * clientsPerConn {
				s.Setup = append(s.Setup, m.pin(0, clientOf(c, i), (c*131+i*17)%packageCount, "1."+strconv.Itoa(i%4)+".0"))
			}
		}
	}
	for i := range n {
		conn := i % conns
		var x Exchange
		switch kind {
		case "listing":
			x = m.listing(conn)
		case "pin":
			x = m.pin(conn, clientOf(conn, i/conns), (i*7)%packageCount, "2."+strconv.Itoa(i%10)+".1")
		case "show":
			x = m.show(conn, clientOf(conn, i/conns))
		case "status":
			x = m.status(conn)
		case "hook":
			x = m.hook(conn, i, true)
		default:
			panic("servercount: no route kind " + kind)
		}
		s.Main = append(s.Main, x)
	}
	return s
}

// MixedScript is n seeded requests of every kind, refusals included, on
// one connection: an over-long body (a pin that would be kept if the
// limit were not checked), a body that is not JSON, an empty client, an
// unknown package, a bad signature, an unknown path and a method no route
// takes.
func MixedScript(seed uint64, n int) Script {
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	m := newModel()
	s := Script{Conns: 1}
	for i := range n {
		client := "c" + strconv.Itoa(r.IntN(6))
		k := r.IntN(packageCount)
		var x Exchange
		switch op := r.IntN(14); op {
		case 0:
			x = m.listing(0)
		case 1, 2, 3:
			x = m.pin(0, client, k, strconv.Itoa(r.IntN(3))+"."+strconv.Itoa(i%10)+".0")
		case 4:
			x = m.refusedPin(0, `{"client":"`+client+`","name":"`+packageName(k)+`","version":"`+strings.Repeat("9", maxBody)+`"}`, 413, "body too large")
		case 5:
			x = m.refusedPin(0, `{"client":`, 400, "bad body")
		case 6:
			x = m.refusedPin(0, `{"client":"","name":"`+packageName(k)+`","version":"1.0.0"}`, 400, "bad body")
		case 7:
			x = m.refusedPin(0, `{"client":"`+client+`","name":"pkx`+strconv.Itoa(k%10)+`","version":"1.0.0"}`, 404, "no package pkx"+strconv.Itoa(k%10))
		case 8, 9:
			x = m.show(0, client)
		case 10:
			x = m.status(0)
		case 11:
			x = m.hook(0, i, r.IntN(2) == 0)
		case 12:
			x = m.noRoute(0, "GET", "/nowhere/"+strconv.Itoa(k))
		default:
			x = m.noRoute(0, "DELETE", "/packages")
		}
		s.Main = append(s.Main, x)
	}
	return s
}
