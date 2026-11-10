package v1

import (
	"encoding/base64"
	"encoding/json"
	"math"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestTOMLCorpus holds the public parse boundary to toml-test's TOML 1.0
// documents and independent JSON values. Unique mutants: duplicate keys
// accepted, array tables use their first element, invalid underscores,
// multiline quotes lost. It runs in process; no Go binary is built.
func TestTOMLCorpus(t *testing.T) {
	b, err := os.ReadFile("testdata/toml-test/corpus.json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Name, Raw string
		Want      json.RawMessage
	}
	if err := json.Unmarshal(b, &rows); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		t.Run(row.Name, func(t *testing.T) {
			text, err := base64.StdEncoding.DecodeString(row.Raw)
			if err != nil {
				t.Fatal(err)
			}
			got, err := TOMLParse(string(text), 0, 0)
			if row.Want == nil {
				if _, ok := err.(TOMLMalformed); !ok {
					t.Fatalf("invalid document accepted: %s, %v", got, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var want, actual any
			if err := json.Unmarshal(row.Want, &want); err != nil {
				t.Fatal(err)
			}
			d := json.NewDecoder(strings.NewReader(got))
			d.UseNumber()
			if err := d.Decode(&actual); err != nil {
				t.Fatal(err)
			}
			want = tomlOracle(t, want)
			if !reflect.DeepEqual(want, actual) {
				t.Fatalf("value differs\nwant %#v\ngot  %#v", want, actual)
			}
		})
	}
}

// tomlOracle removes the corpus's type annotations, not TOML syntax. The
// public Reader maps all four date/time types to canonical strings, and
// nonfinite floats to the same strings as std json.
func tomlOracle(t *testing.T, v any) any {
	t.Helper()
	switch v := v.(type) {
	case []any:
		for i := range v {
			v[i] = tomlOracle(t, v[i])
		}
		return v
	case map[string]any:
		kind, typed := v["type"].(string)
		if !typed {
			for k := range v {
				v[k] = tomlOracle(t, v[k])
			}
			return v
		}
		s := v["value"].(string)
		switch kind {
		case "string":
			return s
		case "datetime", "datetime-local", "date-local", "time-local":
			layout := map[string]string{"datetime": time.RFC3339Nano, "datetime-local": "2006-01-02T15:04:05.999999999", "date-local": "2006-01-02", "time-local": "15:04:05.999999999"}[kind]
			v, err := time.Parse(layout, s)
			if err != nil {
				t.Fatal(err)
			}
			return v.Format(layout)
		case "bool":
			return s == "true"
		case "integer":
			return json.Number(s)
		case "float":
			f, err := strconv.ParseFloat(s, 64)
			if err != nil {
				t.Fatal(err)
			}
			if math.IsNaN(f) {
				return "NaN"
			}
			if math.IsInf(f, 1) {
				return "Infinity"
			}
			if math.IsInf(f, -1) {
				return "-Infinity"
			}
			text := strconv.FormatFloat(f, 'g', -1, 64)
			if !strings.ContainsAny(text, ".eE") {
				text += ".0"
			}
			return json.Number(text)
		}
	}
	t.Fatalf("unknown corpus value %#v", v)
	return nil
}

// TestTOMLFailuresNameLineAndKey is the diagnostic-position seam; the
// corpus checks rejection, not location. Mutants: fail discards line/key;
// a rejected newline points after the byte rather than at it. The rows
// are the smallest repros of inline-key and newline position bugs.
func TestTOMLFailuresNameLineAndKey(t *testing.T) {
	for _, row := range []struct {
		text string
		line int
		key  string
	}{
		{"# config\n[server]\nport = 1\nport = 2\n", 4, "server.port"},
		{"server = { port = ? }", 1, "server.port"},
		{"name = \"no\n", 1, "name"},
		{"# config\n# next\n# \xff", 3, ""},
	} {
		_, err := TOMLParse(row.text, 0, 0)
		e, ok := err.(TOMLMalformed)
		if !ok || e.Line != row.line || e.Key != row.key || e.Reason == "" {
			t.Fatalf("%q: got %#v", row.text, err)
		}
	}
}

// BenchmarkTOMLParse records the parser floor and the hand-Go control
// (the same parser then encoding/json into a typed record). Comparing the
// generated Reader decoder to GoControl is a separate pending run.
func BenchmarkTOMLParse(b *testing.B) {
	text := "name = 'settings'\njobs = 8\n[server]\nhost = 'localhost'\nports = [8000, 8001]\n"
	type server struct {
		Host  string
		Ports []int
	}
	type settings struct {
		Name   string
		Jobs   int
		Server server
	}
	b.Run("Parse", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := TOMLParse(text, 4096, 16); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("GoControl", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			encoded, err := TOMLParse(text, 4096, 16)
			if err != nil {
				b.Fatal(err)
			}
			var v settings
			if err := json.Unmarshal([]byte(encoded), &v); err != nil {
				b.Fatal(err)
			}
		}
	})
}
