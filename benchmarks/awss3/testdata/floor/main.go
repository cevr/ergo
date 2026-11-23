// Command floor is awss3bench's Go-runtime floor (common.md GO-RUNTIME
// FLOOR), run as `floor bench calls N`. Each call is the work a client of
// an in-memory S3 cannot skip: the object found by its key once, each
// header written once to a fixed table and read once from it into the
// output struct (no header map, no parse of a value the call made
// itself), and the 64 KB body handed to the sink with no copy.
package main

import (
	"fmt"
	"os"
	"strconv"
)

type object struct {
	body, etag, contentType, lastModified string
}

type out struct {
	etag, contentType, lastModified string
	contentLength                   int
}

// field is one answer header, in a table of fixed places.
type field struct{ name, value string }

//go:noinline
func answer(objects map[string]*object, key string, table *[4]field) (*object, bool) {
	o, ok := objects[key]
	if !ok {
		return nil, false
	}
	table[0] = field{"ETag", o.etag}
	table[1] = field{"Content-Type", o.contentType}
	table[2] = field{"Last-Modified", o.lastModified}
	table[3] = field{"Content-Length", ""}
	return o, true
}

//go:noinline
func read(table *[4]field, o *object) out {
	return out{etag: table[0].value, contentType: table[1].value, lastModified: table[2].value, contentLength: len(o.body)}
}

func main() {
	if len(os.Args) != 4 || os.Args[1] != "bench" || os.Args[2] != "calls" {
		fmt.Fprintln(os.Stderr, "usage: floor bench calls N")
		os.Exit(2)
	}
	n, err := strconv.Atoi(os.Args[3])
	if err != nil {
		os.Exit(2)
	}
	body := ""
	for i := 0; i < 4096; i++ {
		body += "0123456789abcdef"
	}
	objects := map[string]*object{"2:bk/dir/obj": {body: body, etag: `"e"`, contentType: "application/octet-stream", lastModified: "Tue, 14 Nov 2023 22:13:21 GMT"}}
	var table [4]field
	t, size := 0, 0
	sink := func(p string) { size += len(p) }
	for i := 0; i < n; i++ {
		o, ok := answer(objects, "2:bk/dir/obj", &table)
		if !ok {
			os.Exit(1)
		}
		t += read(&table, o).contentLength
		o, ok = answer(objects, "2:bk/dir/obj", &table)
		if !ok {
			os.Exit(1)
		}
		sink(o.body)
		_ = read(&table, o)
		delete(objects, "2:bk/dir/missing")
	}
	fmt.Println(t + size)
}
