root="$1/../../../.."
mkdir pkg
cp "$root/tools/npm/tn1-lint/lint.js" "$root/tools/npm/tn1-lint/worker.js" "$root/tools/npm/tn1-lint/wire.js" pkg/
cp "$1/../serve/expected.out" serve.out
cp "$1/../lint/expected.out" lint.out
