package daemon

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/merlin-digital/ship/internal/brand"
)

// Client talks to the daemon's API.
type Client struct {
	Home string
	Info *Info
	http *http.Client
}

// ErrUnreachable means the daemon couldn't be reached or started.
var ErrUnreachable = errors.New("daemon unreachable")

// Connect returns a client for a running daemon, or ErrUnreachable.
func Connect(home string) (*Client, error) {
	info, err := ReadInfo(home)
	if err != nil {
		return nil, fmt.Errorf("%w: no %s", ErrUnreachable, filepath.Join(home, InfoFile))
	}
	c := &Client{Home: home, Info: info, http: &http.Client{Timeout: 3 * time.Minute}}
	if err := c.Health(); err != nil {
		return nil, err
	}
	return c, nil
}

// Health checks GET /api/health.
func (c *Client) Health() error {
	hc := &http.Client{Timeout: 2 * time.Second}
	resp, err := hc.Get(c.Info.URL() + "/api/health")
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("%w: health returned %d", ErrUnreachable, resp.StatusCode)
	}
	return nil
}

// Ensure connects to the daemon, starting it detached if needed.
// It warns on stderr when the daemon runs a different version.
func Ensure(home string) (*Client, error) {
	c, err := Connect(home)
	if err == nil {
		if c.Info.Version != brand.Version {
			fmt.Fprintf(os.Stderr, "warning: the running daemon is %s %s but this CLI is %s; run `%s serve --restart`\n", brand.Name, c.Info.Version, brand.Version, brand.Name)
		}
		return c, nil
	}
	if err := SpawnDetached(home, nil); err != nil {
		return nil, fmt.Errorf("%w: couldn't start the daemon: %v", ErrUnreachable, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := Connect(home); err == nil {
			return c, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil, fmt.Errorf("%w: started the daemon but it didn't answer within 5s; see %s", ErrUnreachable, filepath.Join(home, LogFile))
}

// SpawnDetached re-execs `ship serve` in its own session with stdio going
// to daemon.log (rotated at 10MB, keeping 3).
func SpawnDetached(home string, extraArgs []string) error {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	logPath := filepath.Join(home, LogFile)
	rotate(logPath, 10<<20, 3)
	lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer lf.Close()
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	args := append([]string{"serve", "--home", home}, extraArgs...)
	cmd := exec.Command(exe, args...)
	cmd.Stdout, cmd.Stderr = lf, lf
	cmd.Env = append(os.Environ(), brand.HomeEnv+"="+home)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func rotate(path string, max int64, keep int) {
	st, err := os.Stat(path)
	if err != nil || st.Size() < max {
		return
	}
	for i := keep - 1; i >= 1; i-- {
		os.Rename(fmt.Sprintf("%s.%d", path, i), fmt.Sprintf("%s.%d", path, i+1))
	}
	os.Rename(path, path+".1")
	os.Remove(fmt.Sprintf("%s.%d", path, keep+1))
}

// StatusError is an API error response.
type StatusError struct {
	Status int
	Body   APIError
}

func (e *StatusError) Error() string {
	if e.Body.Error.Message != "" {
		return e.Body.Error.Message
	}
	return fmt.Sprintf("daemon returned %d", e.Status)
}

// Do sends a request; in is JSON-encoded, out is decoded from JSON.
func (c *Client) Do(method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.Info.URL()+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Info.Token)
	req.Header.Set("X-Ship-Client", "cli")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		se := &StatusError{Status: resp.StatusCode}
		_ = json.Unmarshal(data, &se.Body)
		return se
	}
	if out != nil && len(data) > 0 {
		if raw, ok := out.(*[]byte); ok {
			*raw = data
			return nil
		}
		return json.Unmarshal(data, out)
	}
	return nil
}

// UIURL returns a browser URL that signs in and lands on path.
func (c *Client) UIURL(path string) string {
	if path == "" {
		path = "/"
	}
	return c.Info.URL() + "/?t=" + c.Info.Token + "&next=" + path
}
