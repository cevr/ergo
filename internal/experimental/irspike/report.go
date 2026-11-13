package irspike

import "ergo.local/prototype/internal/sketch/lower"

// Step is one measured build or run of one path.
type Step struct {
	Path   string `json:"path"`
	Cost   Cost   `json:"cost"`
	Stdout string `json:"stdout,omitempty"`
	Stderr string `json:"stderr,omitempty"` // its first line
	Exit   int    `json:"exit"`
}

// ToolStep is one tool run the -toolexec meter saw.
type ToolStep struct {
	Tool string `json:"tool"`
	Pkg  string `json:"pkg,omitempty"`
	Cost Cost   `json:"cost"`
}

// Report is one program's receipt.
type Report struct {
	IR    lower.IRReport `json:"ir"`
	Build []Step         `json:"build,omitempty"`
	Run   []Step         `json:"run,omitempty"`
}
