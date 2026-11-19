// Command go is work's Go language plugin, level 1 (SEMWS M1): it reads
// work's requests on standard input, one JSON object a line, and writes
// one answer a line, in order (modules/work/work/plugin.ergo). It answers
// UnitsOf by the Splitter of split.go; every other request is
// NotProvided, never an empty answer.
//
//	[language.go]
//	command = "go run ./plugins/go"
//	files = ["*.go"]
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
)

type request struct {
	Tag  string `json:"_tag"`
	File string `json:"file"`
	Text string `json:"text"`
}

// answer is the answer to one request line.
func answer(line string) any {
	var r request
	if err := json.Unmarshal([]byte(line), &r); err != nil || r.Tag == "" {
		return map[string]any{"_tag": "Unanswered", "command": "", "why": "a line that is no Request"}
	}
	if r.Tag != "UnitsOf" {
		return map[string]any{"_tag": "NotProvided", "op": strings.TrimSuffix(r.Tag, "Of")}
	}
	units, err := Units(r.File, r.Text)
	var u Unreadable
	if errors.As(err, &u) {
		return map[string]any{"_tag": "Unreadable", "file": r.File, "why": u.Why}
	}
	return map[string]any{"_tag": "UnitsAre", "units": units}
}

// serve answers every request line of in on out.
func serve(in io.Reader, out io.Writer) error {
	s := bufio.NewScanner(in)
	s.Buffer(make([]byte, 1<<20), 1<<28)
	w := bufio.NewWriter(out)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	for s.Scan() {
		line := s.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		if err := enc.Encode(answer(line)); err != nil {
			return err
		}
	}
	if err := s.Err(); err != nil {
		return err
	}
	return w.Flush()
}

func main() {
	if err := serve(os.Stdin, os.Stdout); err != nil {
		os.Stderr.WriteString("go plugin: " + err.Error() + "\n")
		os.Exit(1)
	}
}
