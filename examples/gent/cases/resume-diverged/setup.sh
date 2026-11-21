#!/bin/sh
# Copies the case's fixtures (testdata/cases/resume-diverged): the world,
# and a session file whose second signal the loop does not take.
set -eu
cp "$1/../../testdata/cases/resume-diverged"/world.json "$1/../../testdata/cases/resume-diverged"/session.log .
