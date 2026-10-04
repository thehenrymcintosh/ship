// Package daemon is the long-running ship process: it owns run state,
// serves the HTTP API and web UI on localhost, and streams live updates.
package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/merlin-digital/ship/internal/agent"
	"github.com/merlin-digital/ship/internal/agent/claude"
	"github.com/merlin-digital/ship/internal/agent/fake"
	"github.com/merlin-digital/ship/internal/brand"
	"github.com/merlin-digital/ship/internal/config"
	"github.com/merlin-digital/ship/internal/engine"
	"github.com/merlin-digital/ship/internal/notify"
	"github.com/merlin-digital/ship/internal/proc"
	"github.com/merlin-digital/ship/internal/store"
)

// Files in the home dir.
const (
	InfoFile = "daemon.json"
	LockFile = "daemon.lock"
	LogFile  = "daemon.log"
)

// Info is daemon.json.
type Info struct {
	PID       int       `json:"pid"`
	Port      int       `json:"port"`
	Token     string    `json:"token"`
	Version   string    `json:"version"`
	StartedAt time.Time `json:"started_at"`
}

// URL is the base URL of the daemon.
func (i *Info) URL() string { return "http://127.0.0.1:" + strconv.Itoa(i.Port) }

// ReadInfo reads daemon.json.
func ReadInfo(home string) (*Info, error) {
	b, err := os.ReadFile(filepath.Join(home, InfoFile))
	if err != nil {
		return nil, err
	}
	var i Info
	if err := json.Unmarshal(b, &i); err != nil {
		return nil, err
	}
	return &i, nil
}

// Running reports whether a daemon holds the lock.
func Running(home string) bool { return store.IsLocked(filepath.Join(home, LockFile)) }

// NewRegistry registers the built-in agent adapters.
func NewRegistry() *agent.Registry {
	reg := agent.NewRegistry()
	reg.Register(fake.New())
	reg.Register(claude.New())
	return reg
}

// EngineOptions builds engine options for a home dir (shared by the daemon
// and foreground runs).
func EngineOptions(home string, pub engine.Publisher, env []string, foreground bool) (engine.Options, error) {
	st, err := store.New(filepath.Join(home, "state"))
	if err != nil {
		return engine.Options{}, err
	}
	userCfg, err := config.Load(home, "")
	if err != nil {
		return engine.Options{}, err
	}
	var notifier notify.Notifier = notify.Nop{}
	if userCfg.Notifications {
		notifier = notify.Default()
	}
	return engine.Options{
		Store:      st,
		Agents:     NewRegistry(),
		LoadConfig: func(repo string) (config.Config, error) { return config.Load(home, repo) },
		Notifier:   notifier,
		BaseEnv:    env,
		Publisher:  pub,
		MaxAgents:  userCfg.MaxAgents,
		Foreground: foreground,
		// Global pipelines live in ~/.ship/pipelines and work in every repo.
		GlobalPipelines: engine.GlobalPipelinesDir(home),
	}, nil
}

// Options configure Serve.
type Options struct {
	Home string
	Port int // 0 = config ui.port
	Log  *slog.Logger
}

// Daemon is a running server.
type Daemon struct {
	home      string
	info      Info
	eng       *engine.Engine
	st        *store.Store
	hub       *Hub
	log       *slog.Logger
	started   time.Time
	stopCh    chan stopReq
	agentVers sync.Map // name → version
}

type stopReq struct {
	force bool
	wait  bool
}

// ErrAlreadyRunning is returned when another daemon holds the lock.
var ErrAlreadyRunning = errors.New(brand.Name + " daemon is already running")

// Serve runs the daemon until a signal or a shutdown request.
func Serve(ctx context.Context, o Options) error {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if err := os.MkdirAll(o.Home, 0o700); err != nil {
		return err
	}
	lock, err := store.TryLock(filepath.Join(o.Home, LockFile))
	if err != nil {
		if errors.Is(err, store.ErrLocked) {
			return ErrAlreadyRunning
		}
		return err
	}
	defer lock.Unlock()

	cfg, err := config.Load(o.Home, "")
	if err != nil {
		return err
	}
	port := o.Port
	if port == 0 {
		port = cfg.UI.Port
	}
	ln, err := listen(port)
	if err != nil {
		return err
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return err
	}
	d := &Daemon{home: o.Home, log: o.Log, started: time.Now(), stopCh: make(chan stopReq, 1)}
	d.info = Info{PID: os.Getpid(), Port: ln.Addr().(*net.TCPAddr).Port, Token: hex.EncodeToString(tokenBytes), Version: brand.Version, StartedAt: d.started.UTC()}

	env := proc.LoginEnv(ctx)
	eo, err := EngineOptions(o.Home, nil, env, false)
	if err != nil {
		return err
	}
	eo.Log = o.Log
	d.st = eo.Store
	inbox := []string{}
	if snaps, err := d.st.List(); err == nil {
		for _, s := range snaps {
			if s.Status.InInbox() {
				inbox = append(inbox, s.ID)
			}
		}
	}
	d.hub = NewHub(inbox)
	eo.Publisher = d.hub
	d.eng = engine.New(eo)
	go d.checkAgents(eo.Agents)

	b, _ := json.MarshalIndent(d.info, "", "  ")
	if err := store.WriteFileAtomic(filepath.Join(o.Home, InfoFile), b, 0o600); err != nil {
		return err
	}
	defer os.Remove(filepath.Join(o.Home, InfoFile))

	n, err := d.eng.Recover()
	if err != nil {
		o.Log.Warn("recovery", "err", err)
	}
	o.Log.Info("daemon started", "url", d.info.URL(), "pid", d.info.PID, "version", brand.Version, "recovered", n)

	srv := &http.Server{Handler: d.routes(), ReadHeaderTimeout: 10 * time.Second}
	srvErr := make(chan error, 1)
	go func() { srvErr <- srv.Serve(ln) }()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sigs)

	select {
	case <-ctx.Done():
	case s := <-sigs:
		o.Log.Info("signal, shutting down", "signal", s)
	case req := <-d.stopCh:
		if req.wait && !req.force && d.eng.Executing() > 0 {
			o.Log.Info("restart requested: waiting for running agent/run steps to finish")
		}
		for req.wait && !req.force && d.eng.Executing() > 0 {
			select {
			case <-time.After(time.Second):
			case <-sigs:
				req.force = true
			}
		}
		o.Log.Info("shutdown requested", "force", req.force)
	case err := <-srvErr:
		return err
	}
	shutCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutCtx)
	if err := d.eng.Shutdown(shutCtx); err != nil {
		o.Log.Warn("engine shutdown", "err", err)
	}
	o.Log.Info("daemon stopped")
	return nil
}

func listen(port int) (net.Listener, error) {
	var lastErr error
	for p := port; p <= port+20; p++ {
		ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(p))
		if err == nil {
			return ln, nil
		}
		lastErr = err
		if port == 0 {
			break
		}
	}
	return nil, fmt.Errorf("no free port in %d–%d: %w", port, port+20, lastErr)
}

func (d *Daemon) checkAgents(reg *agent.Registry) {
	for _, name := range reg.Names() {
		a, _ := reg.Get(name)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		v, err := a.Check(ctx)
		cancel()
		if err != nil {
			v = "unavailable: " + err.Error()
		}
		d.agentVers.Store(name, v)
		d.log.Info("agent cli", "name", name, "version", v)
	}
}
