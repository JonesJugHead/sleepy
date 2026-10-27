package power

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func write(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestNilDirBlocksNothing(t *testing.T) {
	var d *Dir = Open("")
	if d.WakeBlocked() != None || d.AdminStopped() {
		t.Fatal("blocked without a directory")
	}
	if _, _, ok := d.KeepAwake(); ok {
		t.Fatal("keep-awake without a directory")
	}
	if err := d.WriteState(Awake, time.Now(), "x"); err != nil {
		t.Fatal(err)
	}
}

func TestWakeBlocked(t *testing.T) {
	dir := t.TempDir()
	d := Open(dir)
	if d.WakeBlocked() != None {
		t.Fatal("blocked in an empty directory")
	}
	write(t, dir, "admin.json", `{"mode": "auto", "by": "alice"}`)
	if d.WakeBlocked() != None {
		t.Fatal("blocked in auto mode")
	}
	write(t, dir, "admin.json", `{"mode": "stopped", "by": "alice", "at": "2026-10-22T21:30:00+02:00"}`)
	if d.WakeBlocked() != Stopped || !d.AdminStopped() {
		t.Fatal("admin stop ignored")
	}
	backup := write(t, dir, "backup.lock", `{"pid": 1234}`)
	if d.WakeBlocked() != Backup {
		t.Fatalf("got %q with backup.lock", d.WakeBlocked())
	}
	write(t, dir, "restore.lock", `{}`)
	if d.WakeBlocked() != Restore {
		t.Fatalf("got %q with restore.lock", d.WakeBlocked())
	}
	os.Remove(filepath.Join(dir, "restore.lock"))
	old := time.Now().Add(-7 * time.Hour)
	os.Chtimes(backup, old, old)
	if d.WakeBlocked() != Stopped {
		t.Fatalf("stale backup.lock not ignored: %q", d.WakeBlocked())
	}
	write(t, dir, "admin.json", `not json`)
	if d.WakeBlocked() != None {
		t.Fatal("a broken admin.json blocks")
	}
}

func TestKeepAwake(t *testing.T) {
	dir := t.TempDir()
	d := Open(dir)
	until := time.Now().Add(time.Hour).Truncate(time.Second)
	write(t, dir, "keep-awake", `{"until": "`+until.Format(time.RFC3339)+`", "reason": "BlueMap render"}`)
	got, reason, ok := d.KeepAwake()
	if !ok || !got.Equal(until) || reason != "BlueMap render" {
		t.Fatalf("got %v %q %v", got, reason, ok)
	}
	write(t, dir, "keep-awake", `{"until": "`+time.Now().Add(-time.Minute).Format(time.RFC3339)+`"}`)
	if _, _, ok := d.KeepAwake(); ok {
		t.Fatal("expired keep-awake honoured")
	}
	write(t, dir, "keep-awake", `{"until": "tomorrow"}`)
	if _, _, ok := d.KeepAwake(); ok {
		t.Fatal("invalid date honoured")
	}
}

func TestWriteState(t *testing.T) {
	dir := t.TempDir()
	d := Open(dir)
	since := time.Date(2026, 10, 22, 21, 40, 0, 0, time.FixedZone("", 2*3600))
	if err := d.WriteState(SleepingState, since, "0.1.0"); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(dir, "sleepy.json"))
	if err != nil {
		t.Fatal(err)
	}
	var st struct{ State, Since, Heartbeat, Version string }
	if err := json.Unmarshal(content, &st); err != nil {
		t.Fatal(err)
	}
	heartbeat, err := time.Parse(time.RFC3339, st.Heartbeat)
	if st.State != "sleeping" || st.Since != "2026-10-22T21:40:00+02:00" || st.Version != "0.1.0" || err != nil || time.Since(heartbeat) > time.Minute {
		t.Fatalf("sleepy.json: %s", content)
	}
	if _, err := os.Stat(filepath.Join(dir, "sleepy.json.tmp")); !os.IsNotExist(err) {
		t.Fatal("temporary file left behind")
	}
}
