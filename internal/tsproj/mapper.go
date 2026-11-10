package tsproj

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketchfront"
)

// Transform checks the on-disk package with the editor's source as a read-only
// overlay. Check diagnostics belong to the original source, not virtual TS.
func Transform(fileName, content string, options Options) (Output, error) {
	abs, err := filepath.Abs(fileName)
	if err != nil {
		return Output{}, err
	}
	a, err := sketchfront.CheckDirOverlay(filepath.Dir(abs), map[string]string{abs: content})
	if err != nil {
		return Output{}, err
	}
	if len(a.Diagnostics) > 0 {
		o := Output{Text: "export {};\n", Extension: ".ts", Mappings: [][]int{}}
		for _, d := range a.Diagnostics {
			start, length := d.Span.Offset, d.Span.Length
			message := d.Code + ": " + d.Message
			// A dependency's diagnostic has no span in this file. Name its
			// actual source rather than pretending its byte offset is local.
			if !strings.HasSuffix(filepath.ToSlash(abs), "/"+d.File) {
				start, length = 0, 0
				message = d.File + fmt.Sprintf(":%d:%d: ", d.Span.Line, d.Span.Column) + message
			}
			if start < 0 || start+length > len(content) {
				start, length = 0, 0
			}
			o.Diagnostics = append(o.Diagnostics, Diagnostic{Message: message, Start: units(content[:start], options.Encoding), Length: units(content[start:start+length], options.Encoding), Code: 1001})
		}
		return o, nil
	}
	root := a.Root()
	for _, f := range root.Files {
		if strings.HasSuffix(filepath.ToSlash(abs), "/"+f.Rel) {
			o, err := Project(root.Package, f.Syntax, content, options)
			if err != nil {
				return Output{Text: "export {};\n", Extension: ".ts", Mappings: [][]int{}, Diagnostics: []Diagnostic{{Message: err.Error(), Code: 1002}}}, nil
			}
			return o, nil
		}
	}
	return Output{}, fmt.Errorf("%s is not a package source file", fileName)
}

type request struct {
	Version string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// Serve implements TypeScript 7.1 beta's content mapper JSON-RPC protocol.
// Framing is bounded before allocation. Protocol errors fail the process so
// TypeScript reports a mapper failure; they never become a successful empty file.
func Serve(input io.Reader, output io.Writer) error {
	r := bufio.NewReader(input)
	encoding := ""
	projects := map[string]bool{}
	for {
		body, err := readFrame(r)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		var m request
		if err := json.Unmarshal(body, &m); err != nil {
			return fmt.Errorf("invalid mapper request: %w", err)
		}
		if m.Version != "2.0" || len(m.ID) == 0 {
			return fmt.Errorf("mapper expects JSON-RPC 2.0 requests")
		}
		var result any
		var rpcError any
		switch m.Method {
		case "initialize":
			var p struct {
				PositionEncodings []string `json:"positionEncodings"`
			}
			if err := json.Unmarshal(m.Params, &p); err != nil {
				return err
			}
			for _, e := range p.PositionEncodings {
				if e == "utf-8" || e == "utf-16" {
					encoding = e
					break
				}
			}
			if encoding == "" {
				return fmt.Errorf("mapper requires utf-8 or utf-16 positions")
			}
			result = map[string]string{"positionEncoding": encoding, "diagnosticSource": "ergo"}
		case "openProject", "closeProject":
			var p struct {
				ProjectHandle string `json:"projectHandle"`
			}
			if err := json.Unmarshal(m.Params, &p); err != nil {
				return err
			}
			if encoding == "" {
				return fmt.Errorf("mapper not initialized")
			}
			if m.Method == "openProject" {
				projects[p.ProjectHandle] = true
				result = map[string]any{}
			} else {
				delete(projects, p.ProjectHandle)
			}
		case "transform":
			var p struct{ FileName, Content, ProjectHandle string }
			if err := json.Unmarshal(m.Params, &p); err != nil {
				return err
			}
			if !projects[p.ProjectHandle] {
				return fmt.Errorf("mapper project is not open")
			}
			o, err := Transform(p.FileName, p.Content, Options{Encoding: encoding})
			if err != nil {
				rpcError = map[string]any{"code": -32603, "message": err.Error()}
			} else {
				result = o
			}
		default:
			rpcError = map[string]any{"code": -32601, "message": "unknown mapper method " + m.Method}
		}
		response := map[string]any{"jsonrpc": "2.0", "id": m.ID}
		if rpcError != nil {
			response["error"] = rpcError
		} else {
			response["result"] = result
		}
		b, err := json.Marshal(response)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(output, "Content-Length: %d\r\n\r\n%s", len(b), b); err != nil {
			return err
		}
	}
}

const maxFrame = 16 << 20

func readFrame(r *bufio.Reader) ([]byte, error) {
	n := -1
	for used := 0; ; {
		line, err := r.ReadSlice('\n')
		if err != nil {
			if err == io.EOF && used == 0 && len(line) == 0 {
				return nil, io.EOF
			}
			return nil, fmt.Errorf("mapper header: %w", err)
		}
		used += len(line)
		if used > 8192 {
			return nil, fmt.Errorf("mapper header exceeds 8192 bytes")
		}
		s := strings.TrimSpace(string(line))
		if s == "" {
			break
		}
		k, v, ok := strings.Cut(s, ":")
		if !ok {
			return nil, fmt.Errorf("malformed mapper header")
		}
		if strings.EqualFold(k, "Content-Length") {
			if n != -1 {
				return nil, fmt.Errorf("duplicate mapper Content-Length")
			}
			n, err = strconv.Atoi(strings.TrimSpace(v))
			if err != nil || n < 0 || n > maxFrame {
				return nil, fmt.Errorf("invalid mapper Content-Length")
			}
		}
	}
	if n < 0 {
		return nil, fmt.Errorf("mapper Content-Length missing")
	}
	b := make([]byte, n)
	_, err := io.ReadFull(r, b)
	return b, err
}
