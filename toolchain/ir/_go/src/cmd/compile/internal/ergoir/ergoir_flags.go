// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ergoir

import "flag"

// ergoFlag holds the -ergoir flag. It is registered on the command line
// flag set from this file, so Go's own base.CmdFlags is not edited
// (PATCH-MIN M1): base.ParseFlags parses flag.CommandLine, and cmd/go keys
// its build cache on the -gcflags that carry it.
var ergoFlag struct{ IR string }

// Flag is ergoFlag for noder's hooks (noder/ergoir_hook.go).
var Flag = &ergoFlag

func init() {
	flag.StringVar(&ergoFlag.IR, "ergoir", "", "experimental: read typed function bodies from Ergo IR `file`")
}
