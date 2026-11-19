module admit

go 1.27

require golang.org/x/sync v0.24.0

// The module is committed under testdata/sync (semaphore and errgroup
// only), so a build reads no network and no module cache.
replace golang.org/x/sync => ./testdata/sync
