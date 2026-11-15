// Command control (examples/fk-domains/testdata/control) is the hand Go control of fk-domains' target P11 (plan
// §20.5): the registry's RPC handler written by hand in idiomatic Go
// (encoding/json, no framework), answering the same request script as
// `fk-domains bench N` and printing the same line. scripts/opcount runs
// both with `bench N` and refuses the comparison when the lines differ.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"os"
	"strconv"
	"strings"
)

var prices = map[string]float64{
	"com": 12, "dev": 12, "app": 9.99, "io": 34.99, "ai": 80, "xyz": 2.99,
	"org": 9.99, "me": 13.99, "net": 13.5, "tech": 7.99, "space": 1.99, "cloud": 7.99,
	"studio": 21.99, "sh": 33, "design": 39.99, "co": 25.99, "site": 1.99, "online": 3.99,
	"store": 4.99, "agency": 5.99, "world": 3.99, "run": 19.99, "page": 9.99, "tools": 24.99,
}

type listing struct {
	Tag          string   `json:"_tag"`
	Name         string   `json:"name"`
	TLD          string   `json:"tld"`
	Price        *float64 `json:"price,omitempty"`
	RenewalPrice *float64 `json:"renewalPrice,omitempty"`
	Years        *int     `json:"years,omitempty"`
	IsPremium    *bool    `json:"isPremium,omitempty"`
}

type envelope struct {
	Tag     string          `json:"_tag"`
	ID      json.RawMessage `json:"id"`
	RPC     string          `json:"tag"`
	Payload json.RawMessage `json:"payload"`
}

type payload struct {
	Label  string `json:"label"`
	Offset *int   `json:"offset"`
	Limit  *int   `json:"limit"`
}

type exit struct {
	Tag       string          `json:"_tag"`
	RequestID json.RawMessage `json:"requestId"`
	Exit      any             `json:"exit"`
}

type success struct {
	Tag   string    `json:"_tag"`
	Value []listing `json:"value"`
}

type failure struct {
	Tag   string `json:"_tag"`
	Cause []die  `json:"cause"`
}

type die struct {
	Tag    string `json:"_tag"`
	Defect string `json:"defect"`
}

func validLabel(s string) bool {
	if len(s) < 1 || len(s) > 63 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		alnum := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
		if !alnum && !(i > 0 && i < len(s)-1 && c == '-') {
			return false
		}
	}
	return true
}

func search(p payload) ([]listing, string) {
	if !validLabel(p.Label) {
		return nil, "label"
	}
	from, count := 0, len(catalog)
	if p.Offset != nil {
		if *p.Offset < 0 || *p.Offset > len(catalog)-1 {
			return nil, "offset"
		}
		from = *p.Offset
	}
	if p.Limit != nil {
		if *p.Limit < 1 || *p.Limit > len(catalog) {
			return nil, "limit"
		}
		count = *p.Limit
	}
	to := min(from+count, len(catalog))
	out := make([]listing, 0, to-from)
	taken := p.Label == "vercel" || p.Label == "google" || p.Label == "github" || p.Label == "foldkit"
	for _, tld := range catalog[from:to] {
		name := p.Label + "." + tld
		if taken && (tld == "com" || tld == "dev") {
			out = append(out, listing{Tag: "Unavailable", Name: name, TLD: tld})
			continue
		}
		price, ok := prices[tld]
		if !ok {
			price = 19.99
		}
		years, premium := 1, false
		out = append(out, listing{Tag: "Available", Name: name, TLD: tld, Price: &price, RenewalPrice: &price, Years: &years, IsPremium: &premium})
	}
	return out, ""
}

type header struct{ name, value string }

// answer is the handler: the original's refusals, then one Exit per
// Request envelope.
func answer(method, path, contentType string, body []byte) (int, []header, []byte) {
	if (path != "/rpc" && path != "/rpc/") || method != "POST" {
		return 404, nil, nil
	}
	noStore := header{"Cache-Control", "no-store"}
	if !strings.HasPrefix(contentType, "application/json") {
		return 415, []header{noStore}, nil
	}
	if len(body) > 1024 {
		return 413, []header{noStore}, nil
	}
	var envs []envelope
	if t := bytes.TrimSpace(body); len(t) > 0 && t[0] == '[' {
		if err := json.Unmarshal(t, &envs); err != nil {
			return malformed(err)
		}
	} else {
		var e envelope
		if err := json.Unmarshal(t, &e); err != nil {
			return malformed(err)
		}
		envs = []envelope{e}
	}
	var exits []exit
	for _, e := range envs {
		if e.Tag != "Request" {
			continue
		}
		var p payload
		var value []listing
		bad := "payload"
		if e.RPC == "SearchDomains" && json.Unmarshal(e.Payload, &p) == nil {
			value, bad = search(p)
		}
		if bad != "" {
			exits = append(exits, exit{Tag: "Exit", RequestID: e.ID, Exit: failure{Tag: "Failure", Cause: []die{{Tag: "Die", Defect: "bad " + bad}}}})
			continue
		}
		exits = append(exits, exit{Tag: "Exit", RequestID: e.ID, Exit: success{Tag: "Success", Value: value}})
	}
	out, err := json.Marshal(exits)
	if err != nil {
		panic(err)
	}
	return 200, []header{{"Content-Type", "application/json"}, noStore}, out
}

func malformed(err error) (int, []header, []byte) {
	name := "Malformed"
	if _, ok := err.(*json.SyntaxError); ok {
		name = "SyntaxError"
	}
	msg, _ := json.Marshal(err.Error())
	return 200, []header{{"Content-Type", "application/json"}, {"Cache-Control", "no-store"}},
		[]byte(`[{"_tag":"Defect","defect":{"name":"` + name + `","message":` + string(msg) + `}}]`)
}

var scriptLabels = []string{"foldkit", "acme", "google", "f", "domains", "x9"}
var scriptLimits = []int{4, 56, 60, 556}

// scriptBody is request i of the script (server/script.ergo's ScriptBody).
func scriptBody(i int) []byte {
	label := scriptLabels[i%len(scriptLabels)]
	offset := (i * 61) % 556
	limit := scriptLimits[i%len(scriptLimits)]
	return []byte(fmt.Sprintf(`{"_tag":"Request","id":%d,"tag":"SearchDomains","payload":{"label":"%s","offset":%d,"limit":%d},"traceId":"00000000000000000000000000000001","spanId":"0000000000000001","sampled":true,"headers":[]}`, i, label, offset, limit))
}

func main() {
	if len(os.Args) != 3 || os.Args[1] != "bench" {
		fmt.Fprintln(os.Stderr, "usage: control bench N")
		os.Exit(2)
	}
	n, err := strconv.Atoi(os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	var crc uint32
	total := 0
	for i := range n {
		status, hs, body := answer("POST", "/rpc", "application/json", scriptBody(i))
		crc = crc32.Update(crc, crc32.IEEETable, []byte(strconv.Itoa(status)+"\n"))
		for _, h := range hs {
			crc = crc32.Update(crc, crc32.IEEETable, []byte(h.name+": "+h.value+"\n"))
		}
		crc = crc32.Update(crc, crc32.IEEETable, body)
		total += len(body)
	}
	fmt.Printf("%d requests, %d answer bytes, crc32 %d\n", n, total, crc)
}
