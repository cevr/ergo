// Command control is the hand Go control of examples/mdhtml: a CommonMark
// 0.31.2 converter of the same design (blocks in one slice linked by ids,
// then inlines in one slice with the delimiter and bracket stacks, then a
// tree of HTML nodes written with CommonMark's line breaks) and the same
// flags and output.
//
//	control [-unsafe] [-highlight] [-title T] [FILE]
//
// With no FILE it reads standard input. By default raw HTML is left out
// and a script or data link goes nowhere, as in cmark; -unsafe keeps
// them, as the specification's examples expect.
package main

import (
	"fmt"
	"io"
	"os"
)

func main() {
	unsafe, highlight, title, file := false, false, "", ""
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch a := args[i]; a {
		case "-unsafe":
			unsafe = true
		case "-highlight":
			highlight = true
		case "-title":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "mdhtml: -title needs a value")
				os.Exit(2)
			}
			i++
			title = args[i]
		default:
			if file != "" {
				fmt.Fprintln(os.Stderr, "mdhtml: one file at most")
				os.Exit(2)
			}
			file = a
		}
	}
	var src []byte
	var err error
	if file == "" {
		src, err = io.ReadAll(os.Stdin)
	} else {
		src, err = os.ReadFile(file)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "mdhtml:", err)
		os.Exit(1)
	}
	body := render(convert(string(src), unsafe, highlight))
	if title != "" {
		body = "<!doctype html>\n<html>\n<head>\n<meta charset=\"utf-8\">\n<title>" + escape(title) + "</title>\n</head>\n<body>\n" + body + "</body>\n</html>\n"
	}
	os.Stdout.WriteString(body)
}
