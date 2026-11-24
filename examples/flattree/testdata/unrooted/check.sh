#!/bin/sh
set -eu
cd "$(dirname "$0")/../.." || exit 1
if output=$("$1" check ./testdata/unrooted); then
	printf '%s\n' 'Root accepted an empty builder as a document' >&2
	exit 1
fi
printf '%s\n' "$output" | jq -e '
	.checked == false and
	(.diagnostics | length == 1) and
	.diagnostics[0].code == "S104" and
	.diagnostics[0].span.line == 6 and
	(.diagnostics[0].message | contains("Empty"))
'
