// The trampoline to libSystem's renamex_np (exchange_darwin.go), as
// golang.org/x/sys/unix writes its libc trampolines on darwin.

//go:build darwin

#include "textflag.h"

TEXT libc_renamex_np_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_renamex_np(SB)

GLOBL	·libc_renamex_np_trampoline_addr(SB), RODATA, $8
DATA	·libc_renamex_np_trampoline_addr(SB)/8, $libc_renamex_np_trampoline<>(SB)
