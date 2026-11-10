# Runtime URL download

`ergo run examples/download/cmd/download -- --allow http://127.0.0.1:8080 http://127.0.0.1:8080/file`

The binary delegates the host once, with an explicit origin set, response
budgets and redirect hop limit. The network provider captures that value;
the domain workflow can then accept a URL discovered at run time without
Ambient in its row. `--allow` may repeat. `--any` is an explicit open policy.
An origin outside the set is a typed denial, before any request to it.

The recorded cases make no outbound request. The runtime's two-peer model
and the source-boundary property exercise successful and refused redirects,
origin-bound credentials and completed cancellation. A real download is
opt-in through its URL and policy; no endpoint or credential is built in.
