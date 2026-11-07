// Command sections prints the size of the ELF sections of a Go binary that
// a union's representation changes: code (.text), type descriptors and
// constant data (.rodata), the type and itab links (.typelink, .itablink),
// and the line tables (.gopclntab). Usage: sections BINARY.
package main

import (
	"debug/elf"
	"fmt"
	"os"
)

func main() {
	f, err := elf.Open(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "sections:", err)
		os.Exit(1)
	}
	defer f.Close()
	for _, name := range []string{".text", ".rodata", ".typelink", ".itablink", ".gopclntab"} {
		var size uint64
		if s := f.Section(name); s != nil {
			size = s.Size
		}
		fmt.Printf(" %s=%d", name, size)
	}
	fmt.Println()
}
