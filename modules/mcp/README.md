# mcp

An Ergo module of this repository (`modules/`, MK0): a server of the Model Context Protocol (AP2), written in Ergo over std `json` and `io`. A program gives its tools as values and binds a transport in its `cmd/<name>/main.ergo`.

| Package | What |
| --- | --- |
| `mcp` | `Transport` (an operation type with its law), `Tool`, `ToolError`, `UnknownTool`, `Server`, `Call`, `List`, `Handle` (one message to an `Answer`), `Line`, `Serve` |
| `mcp/memory` | `Transport` in memory: the client's messages given at the start, `Sent` for the test |
| `mcp/stdio` | `Transport` over standard input and output, one message per line, made in `main` (it needs `Ambient`) |

## The protocol

JSON-RPC 2.0, one message per line (MCP's stdio transport). The server answers `initialize` (the protocol version `2025-06-18`, the `tools` capability, the server's name and version), `ping`, `tools/list` and `tools/call`. A message with no `id` is a notification and gets no answer.

The protocol has two kinds of failure, and they stay apart:

- A request that the server cannot take is a JSON-RPC error: a message that is not JSON (`-32700`, id `null`), a message with no method (`-32600`), an unknown method (`-32601`), and a call with no name or of an unknown tool (`-32602`).
- A tool that ran and failed (`ToolError`) is a result whose `isError` is `true`, with the failure's message as its text, so the model reads it.

A tool's value goes back as one text block, which holds its JSON.

## Laws

- `Transport.StopsWhenAsked`: a break out of `Messages` ends the read. It runs for `memory.Transport`.
- `ListNamesEveryTool`: `tools/list` names every tool, in order, each once.
- `UnknownToolIsATypedError`: a call of a name no tool has fails `UnknownTool`, and the answer is the JSON-RPC error that names the tool, not a result.
- `memory.CallAnswersOnce`: in a session of generated requests and notifications, each request gets one answer that carries its id, in order, and a notification gets none.
- `memory.Session` is a scripted session: initialize, the initialized notification, the list, a call, a call whose tool fails, an unknown tool, an unknown method, a line that is not JSON, and a ping. Each answer is checked as an exact line.

## Decided

- The `tools/call` arguments and a tool's value are `json.Value`. A program reads the arguments it needs (`Field`), because a tool's schema is data given to the client and is not checked by the server. AP3's `ergo gen mcp` generates typed tools from an Api's operation type.
- `Serve` reads the transport as a sequence, so the stdio transport answers each line as it comes. A session ends when the input ends.
