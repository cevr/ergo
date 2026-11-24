package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// bootstrapGo opens the Go scopes once. Git supplies only the accepted
// revision's source; the worktree contributes classifications for IDs
// already known but cannot contribute an ID or a count.
func bootstrapGo(root, ref string, diagnostics io.Writer) error {
	raw, err := gitOutput(root, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return fmt.Errorf("resolve bootstrap base %s: %w", ref, err)
	}
	commit := strings.TrimSpace(string(raw))
	pinned, err := gitOutput(root, "show", "--end-of-options", commit+":"+ledger)
	if err != nil {
		return fmt.Errorf("read pre-bootstrap ledger at %s: %w", commit, err)
	}
	rows, err := readLedger(bytes.NewReader(pinned))
	if err != nil {
		return err
	}
	if bytes.HasPrefix(pinned, []byte(goLedgerSeal+"\n")) ||
		bytes.Contains(pinned, []byte("\n"+goLedgerSeal+"\n")) {
		return fmt.Errorf("Go scopes already active at %s; use check --base or prune", commit)
	}
	for id := range rows {
		if path.Ext(id.file) == ".go" {
			return fmt.Errorf("Go scopes already active at %s; use check --base or prune", commit)
		}
	}

	name := filepath.Join(root, ledger)
	work, err := os.Open(name)
	if err != nil {
		return err
	}
	prior, err := readLedger(work)
	work.Close()
	if err != nil {
		return err
	}
	sites, err := committedGoSites(root, commit)
	if err != nil {
		return err
	}
	counts := make(map[siteID]int, len(rows)+len(sites))
	for id, row := range rows {
		counts[id] = row.count
	}
	late := map[string]int{}
	for _, s := range sites {
		id := s.id
		row, known := prior[id]
		if !known {
			scope := sourceScope(id.file)
			family := "T"
			if scope == "generator template" {
				family = "X" // a template artifact, not its output's disposition
			}
			row = allowance{family: family, scope: scope}
			late[scope]++
		}
		rows[id] = row
		counts[id]++
	}
	if err := replaceLedger(name, rows, counts); err != nil {
		return err
	}
	fmt.Fprintf(diagnostics, "bootstrapped %d Go occurrences from %s (%d late fixtures, %d late templates)\n",
		len(sites), commit, late["embedded Go tests/property source"], late["generator template"])
	return nil
}

// Read Git blobs directly, not export-attribute-filtered archive files or
// the index/checkout. One cat-file process streams one source at a time.
func committedGoSites(root, commit string) (_ []site, result error) {
	tree, err := gitOutput(root, "ls-tree", "-rz", "--full-tree", commit)
	if err != nil {
		return nil, fmt.Errorf("list Go bootstrap tree %s: %w", commit, err)
	}
	cmd := exec.Command("git", "cat-file", "--batch")
	cmd.Dir = root
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stream, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	defer func() {
		in.Close()
		stream.Close()
		if err := cmd.Wait(); result == nil && err != nil {
			result = fmt.Errorf("read Go bootstrap tree %s: %w", commit, err)
		}
	}()
	var sites []site
	r := bufio.NewReader(stream)
	for item := range bytes.SplitSeq(tree, []byte{0}) {
		meta, name, ok := bytes.Cut(item, []byte{'\t'})
		if !ok || path.Ext(string(name)) != ".go" {
			continue
		}
		fields := bytes.Fields(meta)
		if len(fields) != 3 || string(fields[1]) != "blob" {
			return nil, fmt.Errorf("%s at %s is not a Go source blob", name, commit)
		}
		if _, err := fmt.Fprintln(in, string(fields[2])); err != nil {
			return nil, err
		}
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		header := strings.Fields(line)
		if len(header) != 3 || header[0] != string(fields[2]) || header[1] != "blob" {
			return nil, fmt.Errorf("%s at %s has unexpected cat-file header %q", name, commit, line)
		}
		size, err := strconv.Atoi(header[2])
		if err != nil || size < 0 {
			return nil, fmt.Errorf("%s at %s has invalid blob size %q", name, commit, header[2])
		}
		data := make([]byte, size)
		if _, err := io.ReadFull(r, data); err != nil {
			return nil, err
		}
		if sep, err := r.ReadByte(); err != nil || sep != '\n' {
			return nil, fmt.Errorf("%s at %s has an incomplete blob record", name, commit)
		}
		found, err := embeddedSites(string(name), string(data))
		if err != nil {
			return nil, err
		}
		sites = append(sites, found...)
	}
	return sites, nil
}

func replaceLedger(name string, rows map[siteID]allowance, counts map[siteID]int) error {
	f, err := os.CreateTemp(filepath.Dir(name), ".nou-sites-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0o644); err != nil {
		f.Close()
		return err
	}
	if err := writeLedger(f, rows, counts); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), name)
}
