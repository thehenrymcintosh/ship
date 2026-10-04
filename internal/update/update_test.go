package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tarball(t *testing.T, files map[string]string) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for name, body := range files {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
		tw.Write([]byte(body))
	}
	tw.Close()
	zw.Close()
	return buf.Bytes()
}

// fakeGitHub serves one release like github.com does.
func fakeGitHub(t *testing.T, tag string, archive []byte, sumOverride string) *Updater {
	u := &Updater{Repo: "o/ship", GOOS: "darwin", GOARCH: "arm64"}
	asset := u.AssetName(tag)
	sum := sha256.Sum256(archive)
	hexsum := hex.EncodeToString(sum[:])
	if sumOverride != "" {
		hexsum = sumOverride
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/o/ship/releases/latest":
			if tag == "" {
				http.Redirect(w, r, "/o/ship/releases", http.StatusFound)
				return
			}
			http.Redirect(w, r, "/o/ship/releases/tag/"+tag, http.StatusFound)
		case "/o/ship/releases/download/" + tag + "/checksums.txt":
			fmt.Fprintf(w, "%s  %s\n%s  ship_x_linux_amd64.tar.gz\n", hexsum, asset, strings.Repeat("0", 64))
		case "/o/ship/releases/download/" + tag + "/" + asset:
			w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	u.BaseURL = srv.URL
	return u
}

func TestDownloadAndReplace(t *testing.T) {
	archive := tarball(t, map[string]string{"README.md": "hi", "ship": "#!/bin/sh\necho new\n"})
	u := fakeGitHub(t, "v0.2.0", archive, "")
	ctx := context.Background()
	tag, err := u.Latest(ctx)
	if err != nil || tag != "v0.2.0" {
		t.Fatal(tag, err)
	}
	if u.AssetName(tag) != "ship_0.2.0_darwin_arm64.tar.gz" {
		t.Fatal(u.AssetName(tag))
	}
	bin, err := u.Download(ctx, tag)
	if err != nil || string(bin) != "#!/bin/sh\necho new\n" {
		t.Fatalf("%q %v", bin, err)
	}
	exe := filepath.Join(t.TempDir(), "ship")
	os.WriteFile(exe, []byte("old"), 0o755)
	if err := Replace(exe, bin); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(exe)
	st, _ := os.Stat(exe)
	if string(got) != string(bin) || st.Mode().Perm() != 0o755 {
		t.Fatalf("%q %v", got, st.Mode())
	}
}

func TestChecksumMismatch(t *testing.T) {
	u := fakeGitHub(t, "v0.2.0", tarball(t, map[string]string{"ship": "x"}), strings.Repeat("a", 64))
	if _, err := u.Download(context.Background(), "v0.2.0"); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("want checksum mismatch, got %v", err)
	}
}

func TestNoRelease(t *testing.T) {
	u := fakeGitHub(t, "", nil, "")
	if _, err := u.Latest(context.Background()); err != ErrNoRelease {
		t.Fatalf("want ErrNoRelease, got %v", err)
	}
}

func TestNoBuildForPlatform(t *testing.T) {
	u := fakeGitHub(t, "v0.2.0", tarball(t, map[string]string{"ship": "x"}), "")
	u.GOOS = "plan9"
	if _, err := u.Download(context.Background(), "v0.2.0"); err == nil || !strings.Contains(err.Error(), "no build for plan9") {
		t.Fatal(err)
	}
}

func TestNewer(t *testing.T) {
	cases := []struct {
		tag, cur string
		want     bool
	}{
		{"v0.2.0", "0.1.0", true},
		{"v0.2.0", "0.2.0", false},
		{"v0.2.0", "0.10.0", false},
		{"v1.0.0", "0.9.9", true},
		{"v0.1.0", "0.1.0-dev", true}, // dev builds always update
		{"v0.1.0", "7c3538c", true},
		{"v0.2.0-rc1", "0.1.0", false},
	}
	for _, c := range cases {
		if got := Newer(c.tag, c.cur); got != c.want {
			t.Errorf("Newer(%s, %s) = %v", c.tag, c.cur, got)
		}
	}
}
