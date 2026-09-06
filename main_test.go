package main

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var loc = time.FixedZone("test", 7*3600)

func mustParseTimes(t *testing.T, raw []string) []int {
	t.Helper()
	out, err := parseTimes(raw)
	if err != nil {
		t.Fatalf("parseTimes(%v): %v", raw, err)
	}
	return out
}

func TestParseTimes(t *testing.T) {
	got := mustParseTimes(t, []string{"05:00", "00:00", "17:00", "12:00", "00:00"})
	want := []int{0, 300, 720, 1020}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
	if _, err := parseTimes([]string{"5:00"}); err == nil {
		t.Error("expected error for non-padded 5:00")
	}
	if _, err := parseTimes([]string{"25:00"}); err == nil {
		t.Error("expected error for 25:00")
	}
	if _, err := parseTimes([]string{"abc"}); err == nil {
		t.Error("expected error for abc")
	}
	if _, err := parseTimes(nil); err == nil {
		t.Error("expected error for empty times")
	}
}

func TestSlotHelpers(t *testing.T) {
	times := mustParseTimes(t, []string{"00:00", "05:00", "12:00", "17:00"})

	// A schedule whose first slot is 05:00 has no slot before 05:00 today.
	late := mustParseTimes(t, []string{"05:00", "12:00"})
	now := time.Date(2026, 9, 6, 3, 0, 0, 0, loc)
	if _, ok := latestSlotOnOrBefore(now, late); ok {
		t.Fatalf("expected no slot before 05:00 at 03:00, got ok=true")
	}

	// At 23:59 the last slot of *that* day (17:00) is the latest.
	now = time.Date(2026, 9, 6, 23, 59, 0, 0, loc)
	if slot, ok := latestSlotOnOrBefore(now, times); !ok || slot.Hour() != 17 {
		t.Fatalf("23:59 should match 17:00, got %v ok=%v", slot, ok)
	}

	// Exactly 00:00 -> slot 00:00.
	now = time.Date(2026, 9, 6, 0, 0, 0, 0, loc)
	if slot, ok := latestSlotOnOrBefore(now, times); !ok || slot.Hour() != 0 {
		t.Fatalf("00:00 should match slot 00:00, got %v ok=%v", slot, ok)
	}

	// 05:00:30 -> still the 05:00 slot.
	now = time.Date(2026, 9, 6, 5, 0, 30, 0, loc)
	if slot, ok := latestSlotOnOrBefore(now, times); !ok || slot.Hour() != 5 {
		t.Fatalf("05:00:30 should match 05:00, got %v ok=%v", slot, ok)
	}

	// 17:00 exactly -> next must be tomorrow 00:00 (strictly after).
	now = time.Date(2026, 9, 6, 17, 0, 0, 0, loc)
	next := nextSlotAfter(now, times)
	if !next.Equal(time.Date(2026, 9, 7, 0, 0, 0, 0, loc)) {
		t.Fatalf("next after 17:00 = %v, want next day 00:00", next)
	}

	// After the last slot of the day -> tomorrow 00:00.
	now = time.Date(2026, 9, 6, 23, 59, 0, 0, loc)
	next = nextSlotAfter(now, times)
	if !next.Equal(time.Date(2026, 9, 7, 0, 0, 0, 0, loc)) {
		t.Fatalf("next after 23:59 = %v, want tomorrow 00:00", next)
	}

	// describeNextRuns must not loop forever / panic.
	desc := describeNextRuns(time.Date(2026, 9, 6, 18, 0, 0, 0, loc), times, 6)
	if desc == "" {
		t.Fatal("empty schedule description")
	}
}

func TestGzipRoundTrip(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.sql")
	dst := src + ".gz"
	payload := []byte("CREATE TABLE x (id INT);\nINSERT INTO x VALUES (1),(2),(3);\n")
	if err := os.WriteFile(src, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := gzipFile(src, dst); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	var out bytes.Buffer
	if _, err := out.ReadFrom(zr); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), payload) {
		t.Fatalf("round trip mismatch: got %d bytes want %d", out.Len(), len(payload))
	}
}

func TestObjectKeyAndSanitize(t *testing.T) {
	s := &r2Store{prefix: "backup"}
	ts := time.Date(2026, 9, 6, 0, 0, 0, 0, time.Local)
	if got := s.objectKey("jmdatabase", ts, true); got != "backup/jmdatabase/2026-09-06_00-00-00.sql.gz" {
		t.Fatalf("objectKey = %q", got)
	}
	if got := s.objectKey("jm db/1", ts, false); got != "backup/jm_db_1/2026-09-06_00-00-00.sql" {
		t.Fatalf("objectKey sanitized = %q", got)
	}
}

func TestStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	ts := time.Date(2026, 9, 6, 5, 0, 0, 0, time.UTC)
	if err := saveState(path, ts); err != nil {
		t.Fatal(err)
	}
	got, err := loadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(ts) {
		t.Fatalf("state round trip = %v, want %v", got, ts)
	}
	zero, err := loadState(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil || !zero.IsZero() {
		t.Fatalf("missing state file: got %v err %v, want zero time", zero, err)
	}
}

func TestLoadConfigDefaultsAndEnv(t *testing.T) {
	t.Setenv("MARIADB_PASSWORD", "s3cret")
	t.Setenv("R2_BUCKET", "my-bucket")
	yamlCfg := `database:
  user: "backup_user"
  password: "${MARIADB_PASSWORD}"
schedule:
  times: ["02:00", "14:00"]
cloudflareR2:
  endpoint: "https://abc.r2.cloudflarestorage.com"
  accessKeyId: "AK"
  secretAccessKey: "SK"
  bucket: "${R2_BUCKET}"
`
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(yamlCfg), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database.Password != "s3cret" {
		t.Fatalf("env expansion failed: password = %q", cfg.Database.Password)
	}
	if cfg.R2.Bucket != "my-bucket" {
		t.Fatalf("env expansion failed: bucket = %q", cfg.R2.Bucket)
	}
	// Defaults applied for omitted sections.
	if len(cfg.Database.Databases) != 1 || cfg.Database.Databases[0] != "jmdatabase" {
		t.Fatalf("default database not applied: %v", cfg.Database.Databases)
	}
	if cfg.Database.Host != "127.0.0.1" || cfg.Database.Port != 3306 {
		t.Fatalf("default host/port not applied")
	}
	if cfg.Storage.Compression != "gzip" || cfg.Storage.ObjectPrefix != "backup" {
		t.Fatalf("default storage not applied: %+v", cfg.Storage)
	}
	if cfg.R2.Region != "auto" {
		t.Fatalf("default region not applied: %q", cfg.R2.Region)
	}
	if len(cfg.Schedule.Times) != 2 {
		t.Fatalf("custom times not applied: %v", cfg.Schedule.Times)
	}
}

func TestLoadConfigInvalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(path, []byte("schedule:\n  times: [\"9am\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("expected error for invalid time")
	}
	if _, err := LoadConfig(filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Fatal("expected error for missing file")
	}
}
