set -e
# Both lanes changed row a apart, and theirs removed row b that ours
# changed: two units no merge may choose, so merge-file fails (status 1)
# and names them; Git keeps the file conflicted.
printf '{\n  "about": "a ledger",\n  "mutants": [\n    {"name":"a","to":"1"},\n    {"name":"b","to":"2"},\n    {"name":"c","to":"3"}\n  ]\n}\n' > base.json
printf '{\n  "about": "a ledger",\n  "mutants": [\n    {"name":"a","to":"one"},\n    {"name":"b","to":"two"},\n    {"name":"c","to":"3"}\n  ]\n}\n' > ours.json
printf '{\n  "about": "a ledger",\n  "mutants": [\n    {"name":"a","to":"uno"},\n    {"name":"c","to":"3"}\n  ]\n}\n' > theirs.json
