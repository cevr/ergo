set -e
# Two lanes each added a row at the end of one ledger, and one changed a
# row: Git's text merge clashes on the end; the rows merge does not.
printf '{\n  "about": "a ledger",\n  "mutants": [\n    {"name":"a","to":"1"},\n    {"name":"b","to":"2"}\n  ]\n}\n' > base.json
printf '{\n  "about": "a ledger",\n  "mutants": [\n    {"name":"a","to":"one"},\n    {"name":"b","to":"2"},\n    {"name":"c","to":"3"}\n  ]\n}\n' > ours.json
printf '{\n  "about": "a ledger",\n  "mutants": [\n    {"name":"a","to":"1"},\n    {"name":"b","to":"2"},\n    {"name":"d","to":"4"}\n  ]\n}\n' > theirs.json
