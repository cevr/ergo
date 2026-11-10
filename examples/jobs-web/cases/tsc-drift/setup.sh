# The contract drifts: Running's Of is renamed Total in the Ergo source.
# No generated file stands between, so tsc reads the new contract through
# the content mapper and fails at the page's use of of.
sed 's/Running{Step int, Of int}/Running{Step int, Total int}/' jobs/jobs.ergo > jobs/jobs.ergo.new
mv jobs/jobs.ergo.new jobs/jobs.ergo
grep -q 'Running{Step int, Total int}' jobs/jobs.ergo
