// Package settings holds what can change while Sleepy runs: the reloadable config, and the state
// that persists across restarts in a small file (auto-sleep switch, remembered server status).
package settings

import (
	"encoding/json"
	"errors"
	"os"
	"sync"

	"sleepy/internal/backend"
	"sleepy/internal/config"
	"sleepy/internal/logx"
)

type Store struct {
	mu        sync.Mutex
	cfg       *config.Config
	state     state
	statePath string
	// changed is closed (and replaced) on every change.
	changed chan struct{}
}

type state struct {
	AutoSleep bool `json:"auto_sleep"`
	// ServerStatus is the real server's status as last seen (MOTD, favicon, max players), shown
	// while it sleeps.
	ServerStatus backend.ServerStatus `json:"server_status"`
}

// Open loads the persisted state from statePath (defaults if the file does not exist).
func Open(cfg *config.Config, statePath string) *Store {
	s := &Store{cfg: cfg, state: state{AutoSleep: true}, statePath: statePath, changed: make(chan struct{})}
	content, err := os.ReadFile(statePath)
	if errors.Is(err, os.ErrNotExist) {
		return s
	}
	st := state{AutoSleep: true}
	if err == nil {
		err = json.Unmarshal(content, &st)
	}
	if err != nil {
		logx.Printf("Cannot read %s (%v): using defaults", statePath, err)
		return s
	}
	s.state = st
	if !st.AutoSleep {
		logx.Printf("Auto-sleep is off (sleepy on to enable it again)")
	}
	return s
}

// Snapshot returns the config, the auto-sleep switch and a channel closed at the next change.
func (s *Store) Snapshot() (*config.Config, bool, <-chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg, s.state.AutoSleep, s.changed
}

func (s *Store) Config() *config.Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg
}

func (s *Store) AutoSleep() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.AutoSleep
}

func (s *Store) ServerStatus() backend.ServerStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.ServerStatus
}

func (s *Store) SetConfig(cfg *config.Config) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg = cfg
	s.notifyLocked()
}

// SetAutoSleep switches auto-sleep and persists it. The switch applies even if saving fails.
func (s *Store) SetAutoSleep(on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.AutoSleep = on
	s.notifyLocked()
	return s.saveLocked()
}

// SetServerStatus remembers the real server's status across restarts.
func (s *Store) SetServerStatus(status backend.ServerStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.ServerStatus = status
	if err := s.saveLocked(); err != nil {
		logx.Printf("Cannot save the server status to %s: %v", s.statePath, err)
	}
}

func (s *Store) saveLocked() error {
	content, _ := json.MarshalIndent(s.state, "", "  ")
	return os.WriteFile(s.statePath, append(content, '\n'), 0o644)
}

func (s *Store) notifyLocked() {
	close(s.changed)
	s.changed = make(chan struct{})
}
