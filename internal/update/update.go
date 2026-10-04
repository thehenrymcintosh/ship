// Package update replaces the running binary with a GitHub release:
// it finds the latest tag, downloads the archive for this OS and
// architecture, checks it against the release's checksums.txt, and swaps
// the executable in place.
package update

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/thehenrymcintosh/ship/internal/brand"
)

// Updater talks to GitHub releases.
type Updater struct {
	Repo    string // owner/name
	BaseURL string // https://github.com
	GOOS    string
	GOARCH  string
	HTTP    *http.Client
}

// New returns an updater for the brand's repo on this platform.
func New() *Updater {
	base := "https://github.com"
	if v := os.Getenv(brand.EnvPrefix + "GITHUB_URL"); v != "" {
		base = v // for testing against a local server
	}
	return &Updater{Repo: brand.Repo, BaseURL: base, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
}

func (u *Updater) client() *http.Client {
	if u.HTTP != nil {
		return u.HTTP
	}
	return &http.Client{Timeout: 2 * time.Minute}
}

// ErrNoRelease means the repo has no published release yet.
var ErrNoRelease = errors.New("no release has been published yet")

// Latest returns the newest release tag (e.g. "v0.2.0"). It follows the
// /releases/latest redirect rather than the API, so there's no rate limit.
func (u *Updater) Latest(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, u.BaseURL+"/"+u.Repo+"/releases/latest", nil)
	if err != nil {
		return "", err
	}
	c := *u.client()
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := c.Do(req)
	if err != nil {
		return "", fmt.Errorf("checking for updates: %w", err)
	}
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	i := strings.LastIndex(loc, "/tag/")
	if resp.StatusCode/100 != 3 || i < 0 {
		if resp.StatusCode == http.StatusNotFound || resp.StatusCode/100 == 3 {
			return "", ErrNoRelease
		}
		return "", fmt.Errorf("checking for updates: GitHub returned %s", resp.Status)
	}
	return loc[i+len("/tag/"):], nil
}

// AssetName is the release archive for a version on this platform, as
// named by .goreleaser.yml.
func (u *Updater) AssetName(tag string) string {
	return fmt.Sprintf("%s_%s_%s_%s.tar.gz", brand.Name, strings.TrimPrefix(tag, "v"), u.GOOS, u.GOARCH)
}

func (u *Updater) get(ctx context.Context, tag, name string) ([]byte, error) {
	url := fmt.Sprintf("%s/%s/releases/download/%s/%s", u.BaseURL, u.Repo, tag, name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := u.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading %s: %s", name, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 256<<20))
}

// Download fetches the binary for tag and verifies it against checksums.txt.
func (u *Updater) Download(ctx context.Context, tag string) ([]byte, error) {
	asset := u.AssetName(tag)
	sums, err := u.get(ctx, tag, "checksums.txt")
	if err != nil {
		return nil, err
	}
	want := ""
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == asset {
			want = f[0]
		}
	}
	if want == "" {
		return nil, fmt.Errorf("release %s has no build for %s/%s", tag, u.GOOS, u.GOARCH)
	}
	archive, err := u.get(ctx, tag, asset)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(archive)
	if got := hex.EncodeToString(sum[:]); got != want {
		return nil, fmt.Errorf("checksum mismatch for %s (got %s, want %s)", asset, got, want)
	}
	return extract(archive, brand.Name)
}

func extract(archive []byte, name string) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("archive has no %s binary", name)
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag == tar.TypeReg && filepath.Base(h.Name) == name {
			return io.ReadAll(io.LimitReader(tr, 256<<20))
		}
	}
}

// Replace swaps the executable at path for bin, atomically.
func Replace(path string, bin []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".new*")
	if err != nil {
		return fmt.Errorf("can't write to %s: %w", dir, err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(bin); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// IsRelease reports whether v looks like a release version (1.2.3).
func IsRelease(v string) bool {
	_, ok := parse(v)
	return ok
}

func parse(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		return out, false // pre-release or dev build
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// Newer reports whether release tag is newer than current. A dev build
// (anything that isn't x.y.z) is never considered up to date.
func Newer(tag, current string) bool {
	t, ok := parse(tag)
	if !ok {
		return false
	}
	c, ok := parse(current)
	if !ok {
		return true
	}
	for i := 0; i < 3; i++ {
		if t[i] != c[i] {
			return t[i] > c[i]
		}
	}
	return false
}
