// Command control is awss3bench's hand Go twin (benchmarks/awss3/cmd/awss3bench),
// run as `control bench calls N`. It is the S3 client a person writing Go
// with net/http writes, against the same in-memory handler logic (wf10
// GEN-EMIT): each call builds an *http.Request with the escaped path,
// serves it to an http.HandlerFunc through an httptest.ResponseRecorder
// (no socket), and reads the answer's headers into a struct: ETag,
// Content-Type, Content-Length and Last-Modified parsed, the x-amz-meta-*
// headers collected. GetObject's body goes to a sink. Neither side signs.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type object struct {
	body        string
	meta        map[string]string
	contentType string
	modified    int64
}

type store struct {
	objects map[string]object
	writes  int64
}

const epoch = 1700000000

func etag(body string) string {
	sum := sha256.Sum256([]byte(body))
	return `"` + hex.EncodeToString(sum[:])[:32] + `"`
}

func target(raw string) (string, string, error) {
	bucket, key, _ := strings.Cut(strings.TrimPrefix(raw, "/"), "/")
	b, err := url.PathUnescape(bucket)
	if err != nil {
		return "", "", err
	}
	k, err := url.PathUnescape(key)
	return b, k, err
}

func (s *store) serve(w http.ResponseWriter, r *http.Request) {
	bucket, key, err := target(r.URL.EscapedPath())
	if err != nil {
		w.WriteHeader(400)
		return
	}
	at := strconv.Itoa(len(bucket)) + ":" + bucket + "/" + key
	switch r.Method {
	case "HEAD", "GET":
		o, ok := s.objects[at]
		if !ok {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(404)
			if r.Method == "GET" {
				io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?>`+"\n<Error><Code>NoSuchKey</Code><Message>The specified key does not exist.</Message></Error>")
			}
			return
		}
		h := w.Header()
		h.Set("Last-Modified", time.Unix(epoch+o.modified, 0).UTC().Format(http.TimeFormat))
		h.Set("Content-Length", strconv.Itoa(len(o.body)))
		h.Set("ETag", etag(o.body))
		h.Set("Content-Type", o.contentType)
		for k, v := range o.meta {
			h.Set("x-amz-meta-"+k, v)
		}
		w.WriteHeader(200)
		if r.Method == "GET" {
			io.WriteString(w, o.body)
		}
	case "PUT":
		b, _ := io.ReadAll(r.Body)
		meta := map[string]string{}
		for k, vs := range r.Header {
			if low := strings.ToLower(k); strings.HasPrefix(low, "x-amz-meta-") {
				meta[strings.TrimPrefix(low, "x-amz-meta-")] = vs[0]
			}
		}
		s.writes++
		s.objects[at] = object{body: string(b), meta: meta, contentType: r.Header.Get("Content-Type"), modified: s.writes}
		w.Header().Set("ETag", etag(string(b)))
		w.WriteHeader(200)
	case "DELETE":
		delete(s.objects, at)
		w.WriteHeader(204)
	}
}

// head is what HeadObject and GetObject read from the answer.
type head struct {
	ETag          string
	ContentType   string
	ContentLength int64
	LastModified  time.Time
	Metadata      map[string]string
}

type client struct{ handler http.Handler }

func (c client) do(method, bucket, key string, body string, header http.Header) (*http.Response, error) {
	u := "http://s3.local/" + url.PathEscape(bucket) + "/" + escapeKey(key)
	req, err := http.NewRequest(method, u, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, vs := range header {
		req.Header[k] = vs
	}
	rec := httptest.NewRecorder()
	c.handler.ServeHTTP(rec, req)
	return rec.Result(), nil
}

func escapeKey(key string) string {
	parts := strings.Split(key, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

func readHead(h http.Header) (head, error) {
	out := head{ETag: h.Get("ETag"), ContentType: h.Get("Content-Type"), Metadata: map[string]string{}}
	if v := h.Get("Content-Length"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return out, err
		}
		out.ContentLength = n
	}
	if v := h.Get("Last-Modified"); v != "" {
		t, err := time.Parse(http.TimeFormat, v)
		if err != nil {
			return out, err
		}
		out.LastModified = t
	}
	for k, vs := range h {
		if low := strings.ToLower(k); strings.HasPrefix(low, "x-amz-meta-") {
			out.Metadata[strings.TrimPrefix(low, "x-amz-meta-")] = vs[0]
		}
	}
	return out, nil
}

func (c client) headObject(bucket, key string) (head, error) {
	resp, err := c.do("HEAD", bucket, key, "", nil)
	if err != nil {
		return head{}, err
	}
	if resp.StatusCode != 200 {
		return head{}, fmt.Errorf("status %d", resp.StatusCode)
	}
	return readHead(resp.Header)
}

func (c client) getObject(bucket, key string, sink func([]byte)) (head, error) {
	resp, err := c.do("GET", bucket, key, "", nil)
	if err != nil {
		return head{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return head{}, fmt.Errorf("status %d", resp.StatusCode)
	}
	buf := make([]byte, 32<<10)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			sink(buf[:n])
		}
		if err != nil {
			break
		}
	}
	return readHead(resp.Header)
}

func (c client) putObject(bucket, key, body, contentType string, meta map[string]string) error {
	h := http.Header{}
	h.Set("Content-Type", contentType)
	for k, v := range meta {
		h.Set("x-amz-meta-"+k, v)
	}
	resp, err := c.do("PUT", bucket, key, body, h)
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

func (c client) deleteObject(bucket, key string) error {
	resp, err := c.do("DELETE", bucket, key, "", nil)
	if err != nil {
		return err
	}
	if resp.StatusCode != 204 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

func main() {
	if len(os.Args) != 4 || os.Args[1] != "bench" || os.Args[2] != "calls" {
		fmt.Fprintln(os.Stderr, "usage: control bench calls N")
		os.Exit(2)
	}
	n, err := strconv.Atoi(os.Args[3])
	if err != nil {
		os.Exit(2)
	}
	s := &store{objects: map[string]object{}}
	c := client{handler: http.HandlerFunc(s.serve)}
	if err := c.putObject("bk", "dir/obj", strings.Repeat("0123456789abcdef", 4096), "application/octet-stream", map[string]string{"owner": "bench"}); err != nil {
		os.Exit(1)
	}
	t, size := 0, 0
	for i := 0; i < n; i++ {
		h, err := c.headObject("bk", "dir/obj")
		if err != nil {
			os.Exit(1)
		}
		t += int(h.ContentLength)
		if _, err := c.getObject("bk", "dir/obj", func(p []byte) { size += len(p) }); err != nil {
			os.Exit(1)
		}
		if err := c.deleteObject("bk", "dir/missing"); err != nil {
			os.Exit(1)
		}
	}
	fmt.Println(t + size)
}
