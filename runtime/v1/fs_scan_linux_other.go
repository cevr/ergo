//go:build linux && !amd64 && !arm64

package v1

func scanDirPlatform(path string, scratch *FSDirScratch) ([]FSDirEntry, error) {
	return readDirLstatFallback(path, scratch)
}
