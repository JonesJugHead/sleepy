// Package power is the optional state directory Sleepy shares with a panel and backup scripts:
//
//	admin.json   {"mode": "auto"|"stopped", ...}  written by the panel: "stopped" = never wake
//	backup.lock  present while a backup runs with the server stopped: do not wake
//	restore.lock present while a restore replaces the worlds: do not wake
//	keep-awake   {"until": "<ISO 8601>", "reason": "..."}: do not sleep before that date
//	sleepy.json  {"state", "since", "heartbeat", "version"} written by Sleepy
//
// Files written by others are read on every check, so nothing needs to be reloaded.
package power

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"sleepy/internal/logx"
)

// staleLock: a lock older than this is a leftover from a crashed script.
const staleLock = 6 * time.Hour

// Reasons the server must not be woken up.
const (
	None    = ""
	Stopped = "stopped"
	Backup  = "backup"
	Restore = "restore"
)

// Describe explains, for logs, why WakeBlocked keeps the server down.
func Describe(why string) string {
	switch why {
	case Backup:
		return "backup in progress (backup.lock)"
	case Restore:
		return "restore in progress (restore.lock)"
	case Stopped:
		return "stopped by an administrator (admin.json)"
	}
	return "not blocked"
}

// Dir is the state directory. A nil *Dir is valid: the feature is off and nothing is blocked.
type Dir struct {
	path string
	mu   sync.Mutex
	// warned avoids logging the same unreadable file on every check.
	warned map[string]string
}

func Open(path string) *Dir {
	if path == "" {
		return nil
	}
	return &Dir{path: path, warned: map[string]string{}}
}

func (d *Dir) Path() string {
	if d == nil {
		return ""
	}
	return d.path
}

// AdminStopped reports whether admin.json asks to keep the server stopped.
func (d *Dir) AdminStopped() bool {
	if d == nil {
		return false
	}
	var admin struct {
		Mode string `json:"mode"`
	}
	if !d.readJSON("admin.json", &admin) {
		return false
	}
	return admin.Mode == "stopped"
}

// WakeBlocked returns why the server must not be started now (None if it may).
func (d *Dir) WakeBlocked() string {
	if d == nil {
		return None
	}
	if d.lockHeld("restore.lock") {
		return Restore
	}
	if d.lockHeld("backup.lock") {
		return Backup
	}
	if d.AdminStopped() {
		return Stopped
	}
	return None
}

// KeepAwake returns the date until which the server must stay up, if keep-awake asks for it.
func (d *Dir) KeepAwake() (until time.Time, reason string, ok bool) {
	if d == nil {
		return time.Time{}, "", false
	}
	var keep struct {
		Until  string `json:"until"`
		Reason string `json:"reason"`
	}
	if !d.readJSON("keep-awake", &keep) {
		return time.Time{}, "", false
	}
	until, err := time.Parse(time.RFC3339, keep.Until)
	if err != nil {
		d.warn("keep-awake", "until is not an ISO 8601 date with a time zone: "+keep.Until)
		return time.Time{}, "", false
	}
	if !until.After(time.Now()) {
		return time.Time{}, "", false
	}
	return until, keep.Reason, true
}

func (d *Dir) lockHeld(name string) bool {
	info, err := os.Stat(filepath.Join(d.path, name))
	if err != nil {
		return false
	}
	if time.Since(info.ModTime()) > staleLock {
		d.warn(name, "older than 6 hours, ignored")
		return false
	}
	return true
}

// readJSON reads name into v. A missing file returns false silently, a broken one with a log.
func (d *Dir) readJSON(name string, v any) bool {
	content, err := os.ReadFile(filepath.Join(d.path, name))
	if errors.Is(err, os.ErrNotExist) {
		d.forget(name)
		return false
	}
	if err == nil {
		err = json.Unmarshal(content, v)
	}
	if err != nil {
		d.warn(name, err.Error())
		return false
	}
	d.forget(name)
	return true
}

func (d *Dir) forget(name string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.warned, name)
}

func (d *Dir) warn(name, msg string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.warned[name] == msg {
		return
	}
	d.warned[name] = msg
	logx.Printf("%s: %s", filepath.Join(d.path, name), msg)
}

// State values of sleepy.json.
const (
	Awake         = "awake"
	SleepingState = "sleeping"
	Waking        = "waking"
)

// WriteState writes sleepy.json atomically (temporary file, then rename).
func (d *Dir) WriteState(state string, since time.Time, version string) error {
	if d == nil {
		return nil
	}
	content, _ := json.MarshalIndent(struct {
		State     string `json:"state"`
		Since     string `json:"since"`
		Heartbeat string `json:"heartbeat"`
		Version   string `json:"version"`
	}{state, since.Format(time.RFC3339), time.Now().Format(time.RFC3339), version}, "", "  ")
	path := filepath.Join(d.path, "sleepy.json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(content, '\n'), 0o664); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
