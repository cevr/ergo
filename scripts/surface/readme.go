package main

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

const (
	blockBegin = "<!-- surface: go run ./scripts/surface -write docs/spec/README.md -->"
	blockEnd   = "<!-- surface end -->"
	// probation is the use count under which a built form, at two counts
	// in a row, goes to the owner (PROBATION, q228).
	probation = 10
)

// A Block is the README's surface block as read back: its measures and
// its forms with their counts.
type Block struct {
	Measures []Measure
	Forms    []Form
}

// render writes the block. prev holds the README's earlier forms: their
// current count becomes the previous one.
func render(s *Surface, prev []Form) string {
	var b strings.Builder
	b.WriteString(blockBegin + "\n\n")
	b.WriteString("| Measure | Count | Members |\n| --- | --- | --- |\n")
	for _, m := range s.Measures {
		fmt.Fprintf(&b, "| %s | %d | %s |\n", m.Name, m.Count, codeList(m.Names))
	}
	fmt.Fprintf(&b, "\nUses count tokens, so neither a comment nor a string, in the %d `.ergo` files under `examples/`, `internal/sketch/std/` and `modules/` (testdata left out; %d that do not parse are not counted). A contextual word the syntax tree keeps as a name counts under Names, not as a use. A built form under %d uses at two counts in a row is for the owner: delete or teach.\n\n", s.Files, s.Skipped, probation)
	b.WriteString("| Form | Kind | Uses | Names | Previous | Note |\n| --- | --- | --- | --- | --- | --- |\n")
	for _, f := range s.Forms {
		previous, note := "", ""
		if i := slices.IndexFunc(prev, func(p Form) bool { return p.Name == f.Name && p.Kind == f.Kind }); i >= 0 {
			previous = strconv.Itoa(prev[i].Uses)
			if f.Kind != kindPlanned && f.Uses < probation && prev[i].Uses < probation {
				note = "for the owner: delete or teach"
			}
		}
		if f.Kind == kindPlanned {
			note = "specified, not built"
		}
		names := ""
		if f.Kind == kindWord {
			names = strconv.Itoa(f.Names)
		}
		fmt.Fprintf(&b, "| `%s` | %s | %d | %s | %s | %s |\n", f.Name, f.Kind, f.Uses, names, previous, note)
	}
	b.WriteString("\n" + blockEnd + "\n")
	return b.String()
}

func codeList(names []string) string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = "`" + n + "`"
	}
	return strings.Join(out, ", ")
}

// cut splits text around its surface block.
func cut(text string) (before, block, after string, err error) {
	i := strings.Index(text, blockBegin)
	j := strings.Index(text, blockEnd)
	if i < 0 || j < i {
		return "", "", "", fmt.Errorf("no surface block (%q ... %q)", blockBegin, blockEnd)
	}
	end := j + len(blockEnd)
	if end < len(text) && text[end] == '\n' {
		end++
	}
	return text[:i], text[i:end], text[end:], nil
}

// parseBlock reads the README's surface block back.
func parseBlock(text string) (*Block, error) {
	_, block, _, err := cut(text)
	if err != nil {
		return nil, err
	}
	b := &Block{}
	table := 0
	for _, line := range strings.Split(block, "\n") {
		if !strings.HasPrefix(line, "| ") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		switch {
		case cells[0] == "Measure":
			table = 1
			continue
		case cells[0] == "Form":
			table = 2
			continue
		case strings.HasPrefix(cells[0], "---"):
			continue
		}
		switch table {
		case 1:
			if len(cells) != 3 {
				return nil, fmt.Errorf("measure row %q", line)
			}
			n, err := strconv.Atoi(cells[1])
			if err != nil {
				return nil, fmt.Errorf("measure row %q: %v", line, err)
			}
			m := Measure{Name: cells[0], Count: n}
			for _, name := range strings.Split(cells[2], ", ") {
				if name != "" {
					m.Names = append(m.Names, strings.Trim(name, "`"))
				}
			}
			b.Measures = append(b.Measures, m)
		case 2:
			if len(cells) != 6 {
				return nil, fmt.Errorf("form row %q", line)
			}
			uses, err := strconv.Atoi(cells[2])
			if err != nil {
				return nil, fmt.Errorf("form row %q: %v", line, err)
			}
			b.Forms = append(b.Forms, Form{Name: strings.Trim(cells[0], "`"), Kind: cells[1], Uses: uses})
		}
	}
	return b, nil
}

// compare lists what the README's block says that the count does not:
// a measure's count or members, and the forms with their kinds. Loose
// measures and use counts are not compared.
func compare(old *Block, s *Surface) []string {
	var diffs []string
	for _, m := range s.Measures {
		i := slices.IndexFunc(old.Measures, func(o Measure) bool { return o.Name == m.Name })
		switch {
		case i < 0:
			diffs = append(diffs, fmt.Sprintf("no README row for %q (now %d)", m.Name, m.Count))
		case m.Loose:
		case old.Measures[i].Count != m.Count || !slices.Equal(old.Measures[i].Names, m.Names):
			diffs = append(diffs, fmt.Sprintf("%s: the README has %d %v, the count is %d %v", m.Name, old.Measures[i].Count, old.Measures[i].Names, m.Count, m.Names))
		}
	}
	for _, o := range old.Measures {
		if !slices.ContainsFunc(s.Measures, func(m Measure) bool { return m.Name == o.Name }) {
			diffs = append(diffs, fmt.Sprintf("the README counts %q, which is no measure", o.Name))
		}
	}
	key := func(f Form) string { return f.Kind + " " + f.Name }
	var have, want []string
	for _, f := range old.Forms {
		have = append(have, key(f))
	}
	for _, f := range s.Forms {
		want = append(want, key(f))
	}
	for _, w := range want {
		if !slices.Contains(have, w) {
			diffs = append(diffs, "no README row for the "+w)
		}
	}
	for _, h := range have {
		if !slices.Contains(want, h) {
			diffs = append(diffs, "the README has a row for the "+h+", which the count does not find")
		}
	}
	return diffs
}

// rewrite replaces the README's block with the count, keeping its forms'
// counts as the previous ones.
func rewrite(text string, s *Surface) (string, error) {
	before, _, after, err := cut(text)
	if err != nil {
		return "", err
	}
	old, err := parseBlock(text)
	if err != nil {
		return "", err
	}
	return before + render(s, old.Forms) + after, nil
}
