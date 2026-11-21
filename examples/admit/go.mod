module admit

go 1.27

toolchain go1.27.4

require golang.org/x/sync v0.24.0

// The module is committed under testdata/sync (semaphore
// only), so a build reads no network and no module cache.
replace golang.org/x/sync => ./testdata/sync
