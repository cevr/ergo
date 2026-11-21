root="$(cd "$1/../../../.." && pwd)"
mkdir pkg
cp "$root/tools/npm/tn1-lint/wire.js" "$root/tools/npm/tn1-lint/wire.d.ts" pkg/
printf '%s\n' "$root/examples/tn1-lint/cmd/tn1-lint/serve.ergo" > source.path
