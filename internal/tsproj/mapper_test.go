package tsproj

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMapperOverlayDiagnostics catches r3-overlay (read disk instead of the
// editor buffer) and r3-diagnostic (diagnostics lost or byte positions sent as
// UTF-16). No existing frontend test exercises the mapper protocol seam.
func TestMapperOverlayDiagnostics(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "contract.ergo")
	const disk = "type API { Read() int }\n"
	if err := os.WriteFile(file, []byte(disk), 0600); err != nil {
		t.Fatal(err)
	}
	const source = "// café 🧭\ntype API { Read() Missing }\n"
	for _, enc := range []string{"utf-8", "utf-16"} {
		var input, output bytes.Buffer
		for id, req := range []struct {
			method string
			params any
		}{
			{"initialize", map[string]any{"positionEncodings": []string{enc}}},
			{"openProject", map[string]string{"projectHandle": "p"}},
			{"transform", map[string]string{"projectHandle": "p", "fileName": file, "content": source}},
			{"closeProject", map[string]string{"projectHandle": "p"}},
		} {
			b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": req.method, "params": req.params})
			fmt.Fprintf(&input, "Content-Length: %d\r\n\r\n%s", len(b), b)
		}
		if err := Serve(&input, &output); err != nil {
			t.Fatal(err)
		}
		r := bufio.NewReader(&output)
		var init struct {
			Result struct {
				Encoding string `json:"positionEncoding"`
				Source   string `json:"diagnosticSource"`
			}
		}
		b, err := readFrame(r)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, &init); err != nil {
			t.Fatal(err)
		}
		if init.Result.Encoding != enc || init.Result.Source != "ergo" {
			t.Fatalf("initialize: %s", b)
		}
		if _, err := readFrame(r); err != nil {
			t.Fatal(err)
		}
		b, err = readFrame(r)
		if err != nil {
			t.Fatal(err)
		}
		var response struct{ Result Output }
		if err := json.Unmarshal(b, &response); err != nil {
			t.Fatal(err)
		}
		ds := response.Result.Diagnostics
		if len(ds) != 1 || !strings.Contains(ds[0].Message, "unknown type Missing") {
			t.Fatalf("%s: %s", enc, b)
		}
		if got := sliceUnits(source, ds[0].Start, ds[0].Length, enc); got != "Missing" {
			t.Fatalf("%s: diagnostic at %q, want Missing: %s", enc, got, b)
		}
	}
	b, err := os.ReadFile(file)
	if err != nil || string(b) != disk {
		t.Fatalf("mapper wrote the source: %q %v", b, err)
	}
}

// TestMapperFailsClosed catches r3-frame (unbounded/negative frame allocation)
// and r3-project (a transform on a closed project succeeds). These are failures
// at the mapper boundary, not source diagnostics.
func TestMapperFailsClosed(t *testing.T) {
	for _, row := range []struct{ input, want string }{
		{"Content-Length: -1\r\n\r\n", "invalid mapper Content-Length"},
		{"Content-Length: 16777217\r\n\r\n", "invalid mapper Content-Length"},
		{"Content-Length: 1\r\nContent-Length: 1\r\n\r\nx", "duplicate mapper Content-Length"},
		{"Content-Length: 10\r\n\r\n{}", "unexpected EOF"},
	} {
		if err := Serve(strings.NewReader(row.input), &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), row.want) {
			t.Fatalf("frame %q: %v, want %s", row.input, err, row.want)
		}
	}
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "transform", "params": map[string]string{"projectHandle": "closed"}})
	if err := Serve(strings.NewReader(fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(b), b)), &bytes.Buffer{}); err == nil {
		t.Fatal("transformed unopened project")
	}
}
