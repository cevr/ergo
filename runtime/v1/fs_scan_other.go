//go:build (!linux && !darwin) || (darwin && !amd64 && !arm64)

package v1

func scanDirPlatform(path string, scratch *FSDirScratch) ([]FSDirEntry, error) {
	return readDirLstatFallback(path, scratch)
}
