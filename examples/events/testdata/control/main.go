// Command control is the hand Go control of examples/events for
// scripts/opcount: it tallies a JSON array of events from standard input
// as `events stream` does, element by element with encoding/json's
// Decoder (Token, More, Decode), and prints the same report. With
// `whole` it reads the input whole and decodes it into a slice, as
// `events whole` does.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

type event struct {
	Tag    string `json:"_tag"`
	ID     int    `json:"id"`
	At     int    `json:"at"`
	Bytes  int    `json:"bytes"`
	Reason string `json:"reason"`
}

type tally struct{ opened, sent, bytes, closed, failed int }

func (t *tally) add(e event) error {
	switch e.Tag {
	case "Opened":
		t.opened++
	case "Sent":
		t.sent++
		t.bytes += e.Bytes
	case "Closed":
		t.closed++
	case "Failed":
		t.failed++
	default:
		return fmt.Errorf("no event kind %s", e.Tag)
	}
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "control:", err)
		os.Exit(1)
	}
}

func run() error {
	var t tally
	in := bufio.NewReaderSize(os.Stdin, 65536)
	if len(os.Args) > 1 && os.Args[1] == "whole" {
		text, err := io.ReadAll(in)
		if err != nil {
			return err
		}
		var es []event
		if err := json.Unmarshal(text, &es); err != nil {
			return err
		}
		for _, e := range es {
			if err := t.add(e); err != nil {
				return err
			}
		}
	} else {
		d := json.NewDecoder(in)
		if _, err := d.Token(); err != nil {
			return err
		}
		for d.More() {
			var e event
			if err := d.Decode(&e); err != nil {
				return err
			}
			if err := t.add(e); err != nil {
				return err
			}
		}
		if _, err := d.Token(); err != nil {
			return err
		}
	}
	fmt.Printf("opened %d\nsent %d (%d bytes)\nclosed %d\nfailed %d\nopen %d\n",
		t.opened, t.sent, t.bytes, t.closed, t.failed, t.opened-t.closed-t.failed)
	return nil
}
