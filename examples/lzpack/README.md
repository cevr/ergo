# lzpack

An LZ77-style compressor in Ergo: a hash-chain match finder, a block format of literals and copies, and a decoder that keeps a window of the last 64 KiB as a ring. It is a high-performance program of the corpus wave (HP2): its match and copy loops are index loops whose bounds the lowering's facts should prove, and its hand Go control is the bar.

```sh
lzpack pack FILE          # FILE packed, to standard output
lzpack unpack FILE        # the bytes FILE packs, to standard output
lzpack gen KIND N SEED    # N seeded bytes: text, binary, zeros or random
```

## The format

```
stream   = "LZPK" block* end
block    = uvarint(n) sequence*        n raw bytes, 1 to 65536
end      = uvarint(0)
sequence = token ext(L)? literal*L [offset ext(M)?]
token    = L<<4 | M, each a nibble; 15 means that ext bytes follow
ext      = 255* last                   the length is 15 plus the sum
offset   = uint16 little-endian, 1 to 65535, at most the bytes so far
```

A sequence writes `L` literal bytes, then, unless they end the block, copies `M+4` bytes from `offset` bytes back. A copy may overlap what it writes (a run). A block that ends in literals ends with a sequence whose `M` is 0 and that has no offset. A block header is at most three bytes.

Unpack refuses a stream with `Corrupt{Offset, Reason}`, and checks in this order: the magic (`BadMagic`, offset 0); each block header (`BlockTooLong` at the header); in each sequence the literal length against the block (`Overrun` at the token), then the literals against the stream, then a nibble `M` after the block's last literals (`StrayMatch`), then the offset against the bytes so far (`BadOffset`), then the copy against the block (`Overrun`); bytes after the end mark (`Trailing`). A read past the end of the stream is `Truncated` at the stream's length.

`Pack` is greedy: at each position it walks the chain of earlier positions whose first four bytes hash alike, newest first, at most 16 of them and none 65,536 or more bytes back, and takes the longest match of at least four bytes within the block (the newest of equal ones). Every earlier position whose four bytes are in the input is in a chain before the search, so the stream is a function of the input: the hand Go control and the reference in `cmd/ergo` write the same bytes.

## What it shows

- **Typed failures, never a defect.** Every malformed stream is a `Corrupt` value whose `Reason` is a closed union, matched exhaustively by `Explain`. The law `MalformedIsAFailure` feeds Unpack noise after the magic and packed streams that are cut short or have a byte changed: none is a defect.
- **The frame is a machine.** `Framing` steps once per block: the magic, the blocks, the end mark, and nothing after it. A refusal at the frame's level (`BadMagic`, `BlockTooLong`, `Trailing`) is the machine's `Done`; its laws are decided on its graph (`CanEnd`, `InOrder`) and by runs (`TotalIsTheBlocks`, `LongBlockIsRefused`).
- **Laws about the codec.** `UnpackOfPackIsInput`; `NeverReadsOutsideWindow` (every copy Pack writes starts 1 to 65,535 bytes back and within the bytes so far, on inputs with a repeat farther back than the window); `WalkAgreesWithUnpack`.
- **A streaming window.** The decoder reads its copies from a 64 KiB ring (`[65536]byte`), not from its output, so it would run on a stream of any length in constant memory. The output goes to a `strings.Builder`.

## Layout

- `lz/`: the codec. `format.ergo` (the format, the failures), `pack.ergo` (the chain and the parse), `unpack.ergo` (the decoder), `frame.ergo` (the frame machine), `walk.ergo` (the steps of a stream, for the laws), `laws.ergo`, `source.ergo` (the `Source` contract and its law).
- `corpus/`: the seeded inputs (SplitMix64): text, 16-byte records, zeros and noise.
- `mapped/`: `Source` over the host's files, mapped read-only. `memory/`: `Source` over texts in memory.
- `cmd/lzpack/main.ergo`: binds `mapped` over `fs.NewHost()` and the host's standard output.

## The Go control

`cmd/ergo/testdata/lzpack/control` is the same format and parse in hand Go (the input mapped, 32- and 64-bit loads, one output slice, a copy from the output itself). `TestLzpackAgreesWithControl` (cmd/ergo, full tier) checks that both write what a plain reference writes, on seeded inputs and corrupted streams.

Gaps (bytes): Ergo has no conversion of a `[]byte` to a `string`, so the decoder writes its output to a `strings.Builder` byte by byte and keeps a ring for its window, where the control copies from its output slice.
