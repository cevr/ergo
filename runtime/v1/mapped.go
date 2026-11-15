package v1

import "strings"

// The pure bodies of std fs's mapped texts (wf10 MAPPED): a Mapped held in
// memory and the copy of a text. Mapped and FSMapFile are in host.go.

// FSMapText is the body of fs.MapText: a Mapped whose text is text, held
// in memory. Its Close releases nothing.
//
//ergo:pure
func FSMapText(text string) *Mapped { return &Mapped{text: text} }

// FSClone is the body of fs.Clone: a copy of the text t, which outlives
// the Mapped t borrows.
//
//ergo:pure
func FSClone(t string) string { return strings.Clone(t) }
