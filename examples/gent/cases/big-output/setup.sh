#!/bin/sh
# Copies the case's JSON files into the directory the case runs in.
set -eu
cp "$1"/*.json .
