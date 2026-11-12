// Command driver is the comparison driver of TestSelfhostBridgeAgrees. Its
// module path, ergo.local/prototype/selfhostprobe, lets it import the Go
// parser. It reads framed sources on stdin ("<len>:<bytes>" each) and writes,
// for each, two framed projections on stdout: the one it builds from
// syntax.Parse's tree with reflection, and the one the Ergo library
// formatapi builds from the tree it decoded from the adapter's encoding. The two
// paths share only the parser.
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

	"ergo.local/prototype/internal/sketch/syntax"
	"formatapi"
)

func main() {
	in := bufio.NewReader(os.Stdin)
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	for {
		src, err := frame(in)
		if err == io.EOF {
			return
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "driver:", err)
			os.Exit(2)
		}
		goSide := project(src)
		ergoSide, err := formatapi.Project(context.Background(), src)
		if err != nil {
			ergoSide = "ergo error " + err.Error() + "\n"
		}
		fmt.Fprintf(out, "%d:%s%d:%s", len(goSide), goSide, len(ergoSide), ergoSide)
	}
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
