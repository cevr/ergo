#!/bin/sh
# Copies the case's JSON fixtures (testdata/cases/<case>) into the directory the case runs in.
set -eu
cp "$1/../../testdata/cases/$(basename "$1")"/*.json .
