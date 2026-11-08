#!/bin/sh
# The original is okra's `okra keys` (see origin), the okra on PATH. Each
# case runs it with HOME=. in the case's own directory, so it reads and
# writes only the .okra/keys.json that the case's setup.sh makes.
exec okra "$@"
