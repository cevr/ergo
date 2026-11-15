// Command driver is the comparison driver of the selfhostcheck properties.
// Its module path, ergo.local/prototype/selfhostprobe, lets it import the
// Go parser and the Go formatter. It reads framed values on stdin
// ("<len>:<bytes>" each): first the mode, then the cases of that mode, and
// writes framed answers on stdout:
//
//   - project (TestSelfhostBridgeAgrees): a source; answers the projection
//     it builds from syntax.Parse's tree with reflection, and the one the
//     Ergo library formatapi builds from the tree it decoded.
//   - format (TestSelfhostFormatAgrees): a source and a limit; answers the
//     outcome of the Go formatter (format.SourceBounded) and the outcome of
//     the Ergo formatter (formatapi.Format), both in one encoding.
//   - laws (TestSelfhostFormatAgrees): a source; answers the Ergo verdicts
//     of the formatter's laws (formatapi.Laws).
//   - steps (TestSelfhostLayoutModel): an encoded command list and a limit;
//     answers the Ergo layout's outcome (formatapi.Steps).
//
// The Go and Ergo paths share only the parser; the driver makes no layout
// decision.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/format"
	"ergo.local/prototype/internal/sketch/syntax"
	"formatapi"
)

func main() {
	in := bufio.NewReader(os.Stdin)
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	mode, err := frame(in)
	if err != nil {
		fail(err)
	}
	ctx := context.Background()
	for {
		first, err := frame(in)
		if err == io.EOF {
			return
		}
		if err != nil {
			fail(err)
		}
		var answers []string
		switch mode {
		case "project":
			e, err := formatapi.Project(ctx, first)
			answers = []string{project(first), ergo(e, err)}
		case "format":
			limit := number(in)
			g, gerr := format.SourceBounded(first, limit)
			e, err := formatapi.Format(ctx, first, limit)
			answers = []string{outcome(g, gerr), ergo(e, err)}
		case "laws":
			e, err := formatapi.Laws(ctx, first)
			answers = []string{ergo(e, err)}
		case "steps":
			limit := number(in)
			e, err := formatapi.Steps(ctx, first, limit)
			answers = []string{ergo(e, err)}
		default:
			fail(fmt.Errorf("unknown mode %q", mode))
		}
		for _, a := range answers {
			fmt.Fprintf(out, "%d:%s", len(a), a)
		}
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "driver:", err)
	os.Exit(2)
}

// ergo is an Ergo library answer; an error of the call itself (a defect)
// is its own outcome, never equal to a Go one.
func ergo(s string, err error) string {
	if err != nil {
		return "ergo error " + err.Error() + "\n"
	}
	return s
}

// number reads one framed decimal.
func number(in *bufio.Reader) int {
	s, err := frame(in)
	if err != nil {
		fail(err)
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		fail(err)
	}
	return n
}

// outcome is the encoding of a Go formatter result that formatapi.Format
// writes for the Ergo one: "ok\n" and the text, the located syntax failure,
// or the refused limit.
func outcome(text string, err error) string {
	if err == nil {
		return "ok\n" + text
	}
	var se *syntax.Error
	if errors.As(err, &se) {
		return fmt.Sprintf("syntax %s %d:%s %d %d %d %d\n", se.Code, len(se.Message), se.Message, se.Offset, se.Length, se.Line, se.Column)
	}
	var le *format.LimitError
	if errors.As(err, &le) {
		return fmt.Sprintf("limit %d\n", le.Limit)
	}
	return "go error " + err.Error() + "\n"
}

// frame reads one "<len>:<bytes>" frame.
func frame(in *bufio.Reader) (string, error) {
	head, err := in.ReadString(':')
	if err != nil {
		if err == io.EOF && head == "" {
			return "", io.EOF
		}
		return "", err
	}
	n, err := strconv.Atoi(strings.TrimSuffix(head, ":"))
	if err != nil {
		return "", err
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(in, b); err != nil {
		return "", err
	}
	return string(b), nil
}

var posType = reflect.TypeFor[syntax.Pos]()

// project is the projection of src built from the parser's tree
// (docs: SH0 spec, Projection).
func project(src string) string {
	f, err := syntax.Parse(src)
	var b strings.Builder
	if err != nil {
		var se *syntax.Error
		if !errors.As(err, &se) {
			return "go error " + err.Error() + "\n"
		}
		fmt.Fprintf(&b, "syntax %s %d:%s %d %d %d %d\nsource %d:%s\n", se.Code, len(se.Message), se.Message, se.Offset, se.Length, se.Line, se.Column, len(src), src)
		return b.String()
	}
	p := projector{file: f, b: &b}
	p.value(reflect.ValueOf(f), 0)
	return b.String()
}

type projector struct {
	file *syntax.File
	b    *strings.Builder
}

// value writes the rest of the line that holds v, and v's lines below it.
func (p projector) value(v reflect.Value, depth int) {
	if v.Type() == posType {
		pos := syntax.Pos(v.Int())
		if pos == syntax.NoPos {
			p.b.WriteString("-\n")
			return
		}
		line, column := p.file.Position(pos)
		fmt.Fprintf(p.b, "@%d %d:%d\n", pos, line, column)
		return
	}
	switch v.Kind() {
	case reflect.Bool:
		fmt.Fprintf(p.b, "%t\n", v.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		fmt.Fprintf(p.b, "%d\n", v.Int())
	case reflect.String:
		fmt.Fprintf(p.b, "%d:%s\n", v.Len(), v.String())
	case reflect.Interface:
		if v.IsNil() {
			p.b.WriteString("-\n")
			return
		}
		p.value(v.Elem(), depth)
	case reflect.Pointer:
		if v.IsNil() {
			p.b.WriteString("-\n")
			return
		}
		e := v.Elem()
		t := e.Type()
		p.b.WriteString("{" + t.Name() + "}\n")
		for i := range t.NumField() {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			p.b.WriteString(strings.Repeat("  ", depth+1) + f.Name + " ")
			p.value(e.Field(i), depth+1)
		}
	case reflect.Slice:
		fmt.Fprintf(p.b, "[%d]\n", v.Len())
		for i := range v.Len() {
			p.b.WriteString(strings.Repeat("  ", depth+1))
			p.value(v.Index(i), depth+1)
		}
	default:
		panic(fmt.Sprintf("projection: no rule for %s", v.Type()))
	}
}
