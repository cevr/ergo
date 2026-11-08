#!/bin/sh
# The original is okra's `okra how` (see origin), the okra on PATH. It
# embeds its guides at build time, so it reads none of the files that a
# case's setup.sh copies; the port reads those copies.
exec okra "$@"
