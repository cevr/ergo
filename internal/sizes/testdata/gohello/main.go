// hello prints one line, as the Ergo hello does: one write to standard output.
package main

import "os"

func main() {
	_, _ = os.Stdout.WriteString("hello" + "\n")
}
