#!/bin/sh
# Copies the case's fixtures (testdata/cases/resume): the world, and a
# session file cut while the approved shell call ran.
set -eu
cp "$1/../../testdata/cases/resume"/world.json "$1/../../testdata/cases/resume"/session.log .
