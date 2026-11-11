# The contract drifts: BadCost's Capacity is renamed Max in the Ergo
# source. No generated file stands between, so tsc reads the new contract
# through the content mapper and fails at the gateway's use of capacity.
sed 's/type BadCost { Cost int, Capacity int }/type BadCost { Cost int, Max int }/' limits/limits.ergo > limits/limits.ergo.new
mv limits/limits.ergo.new limits/limits.ergo
grep -q 'type BadCost { Cost int, Max int }' limits/limits.ergo
