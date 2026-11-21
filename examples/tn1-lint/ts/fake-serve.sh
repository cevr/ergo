#!/bin/sh
# fake-serve.sh serve: a tn1-lint serve that answers each request line with
# the line the file $FAKE_REPLY holds at that moment, for ts/decode.ts's
# runs of lintSync.
[ "$1" = serve ] || exit 2
while IFS= read -r _; do
	cat "$FAKE_REPLY"
done
