package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// maxSeedBytes bounds a seed download; today's seed is about 23 MB.
const maxSeedBytes = 512 << 20

// errNoRelease is a download that found no seed: the host answered, but has
// no such release asset (not yet published), or could not be reached. The
// driver then rebuilds the seed from its source. A download that returned
// other bytes than the lock's digest is never this: it is refused.
var errNoRelease = errors.New("no seed release to download")

// seedURL expands the lock's url template for platform.
func seedURL(l seedLock, platform string) string {
	goos, goarch, _ := strings.Cut(platform, "/")
	return strings.NewReplacer("{seed}", l.Seed, "{goos}", goos, "{goarch}", goarch).Replace(l.URL)
}

// download fetches the seed for platform from the lock's url into out, and
// keeps it only when its sha256 is want.
func (d driver) download(l seedLock, platform, want, out string) error {
	url := seedURL(l, platform)
	client := d.client
	if client == nil {
		// The timeout only stops a hung transfer.
		client = &http.Client{Timeout: 10 * time.Minute}
	}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("%w: %v", errNoRelease, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: GET %s: %s", errNoRelease, url, resp.Status)
	}
	f, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, maxSeedBytes+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("%w: GET %s: %v", errNoRelease, url, err)
	}
	if n > maxSeedBytes {
		return fmt.Errorf("GET %s: the seed is larger than %d bytes; refused", url, maxSeedBytes)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("GET %s: the downloaded %s for %s has sha256 %s, but %s says %s; refused (rebuild it from source with -from-source to compare)", url, l.Seed, platform, got, lockFile, want)
	}
	return nil
}
