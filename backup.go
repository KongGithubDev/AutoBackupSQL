package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Backuper performs mysqldump -> compress -> R2 upload for every database.
type Backuper struct {
	cfg   *Config
	store *r2Store
	log   *Logger
}

func NewBackuper(cfg *Config, store *r2Store, log *Logger) *Backuper {
	return &Backuper{cfg: cfg, store: store, log: log}
}

// findMySQLDump resolves the mysqldump executable.
func findMySQLDump(explicit string) (string, error) {
	candidates := []string{}
	if explicit != "" {
		candidates = append(candidates, explicit)
	}
	if p, err := exec.LookPath("mysqldump"); err == nil {
		return p, nil
	}
	// Common Windows install locations (checked from newest-looking path last).
	patterns := []string{
		`C:\Program Files\MariaDB*\bin\mysqldump.exe`,
		`C:\Program Files\MySQL\*\bin\mysqldump.exe`,
		`C:\Program Files\MySQL\MySQL Server*\bin\mysqldump.exe`,
		`C:\xampp\mysql\bin\mysqldump.exe`,
		`C:\laragon\bin\mariadb*\bin\mysqldump.exe`,
		`C:\laragon\bin\mysql*\bin\mysqldump.exe`,
		`C:\wamp64\bin\mariadb*\bin\mysqldump.exe`,
		`C:\wamp64\bin\mysql*\bin\mysqldump.exe`,
	}
	for _, pat := range patterns {
		if m, _ := filepath.Glob(pat); len(m) > 0 {
			candidates = append(candidates, m...)
		}
	}
	sort.Strings(candidates) // ascending: prefer the highest version last
	for i := len(candidates) - 1; i >= 0; i-- {
		if fileExists(candidates[i]) {
			return candidates[i], nil
		}
	}
	if explicit != "" {
		return "", fmt.Errorf("mysqldump not found at configured path %q and not on PATH", explicit)
	}
	return "", fmt.Errorf("mysqldump.exe not found: set database.mysqldumpPath in config.yaml (e.g. C:\\Program Files\\MariaDB 11.4\\bin\\mysqldump.exe)")
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

// RunOnce backs up every configured database and then applies retention.
// Returns the R2 object keys that were uploaded.
func (b *Backuper) RunOnce(ctx context.Context, startedAt time.Time) ([]string, error) {
	dumpPath, err := findMySQLDump(b.cfg.Database.MySQLDumpPath)
	if err != nil {
		return nil, err
	}
	b.log.Info("using mysqldump: %s", dumpPath)

	uploaded := []string{}
	for _, db := range b.cfg.Database.Databases {
		key, err := b.backupDatabase(ctx, dumpPath, db, startedAt)
		if err != nil {
			return uploaded, err
		}
		uploaded = append(uploaded, key)
	}

	if b.cfg.Storage.RetentionDays > 0 {
		cutoff := time.Now().AddDate(0, 0, -b.cfg.Storage.RetentionDays)
		n, err := b.store.deleteOlderThan(ctx, cutoff, b.log)
		if err != nil {
			b.log.Error("retention cleanup failed: %v", err)
		} else if n > 0 {
			b.log.Info("retention: removed %d object(s) older than %d day(s)", n, b.cfg.Storage.RetentionDays)
		}
	}
	return uploaded, nil
}

// backupDatabase dumps a single database and uploads it to R2.
func (b *Backuper) backupDatabase(ctx context.Context, dumpPath, db string, startedAt time.Time) (string, error) {
	gzipped := b.cfg.Storage.Compression == "gzip"

	tmpDir, err := os.MkdirTemp("", "jmdb-dump-*")
	if err != nil {
		return "", fmt.Errorf("cannot create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	rawSQL := filepath.Join(tmpDir, db+".sql")
	gzFile := rawSQL + ".gz"

	b.log.Info("backup started: database=%s", db)
	if err := b.runMySQLDump(ctx, dumpPath, db, rawSQL); err != nil {
		return "", fmt.Errorf("database %q dump failed: %w", db, err)
	}
	sizeRaw, _ := os.Stat(rawSQL)
	if sizeRaw != nil {
		b.log.Info("dump ok: %s (%d bytes) -> compressing", db, sizeRaw.Size())
	}

	if gzipped {
		if err := gzipFile(rawSQL, gzFile); err != nil {
			return "", fmt.Errorf("gzip failed for %q: %w", db, err)
		}
		if err := os.Remove(rawSQL); err != nil {
			return "", fmt.Errorf("cannot remove temp file: %w", err)
		}
	}

	key := b.store.objectKey(db, startedAt, gzipped)
	uploadPath := rawSQL
	if gzipped {
		uploadPath = gzFile
	}
	info, _ := os.Stat(uploadPath)
	b.log.Info("uploading to R2: %s (%d bytes)", key, info.Size())
	if err := b.store.upload(ctx, key, uploadPath); err != nil {
		return "", err
	}
	b.log.Info("backup finished: database=%s object=s3://%s/%s", db, b.store.bucket, key)
	return key, nil
}

// runMySQLDump executes mysqldump for one database into outFile.
// The password is passed through the MYSQL_PWD environment variable so it
// never appears on the visible command line.
func (b *Backuper) runMySQLDump(ctx context.Context, dumpPath, db, outFile string) error {
	args := []string{
		"--host=" + b.cfg.Database.Host,
		"--port=" + strconv.Itoa(b.cfg.Database.Port),
		"--user=" + b.cfg.Database.User,
		"--default-character-set=utf8mb4",
	}
	args = append(args, b.cfg.Database.ExtraDumpOptions...)
	args = append(args, db)

	cmd := exec.CommandContext(ctx, dumpPath, args...)
	cmd.Env = append(os.Environ(), "MYSQL_PWD="+b.cfg.Database.Password)
	f, err := os.Create(outFile)
	if err != nil {
		return err
	}
	defer f.Close()
	cmd.Stdout = f
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf

	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errBuf.String())
		if msg != "" {
			return fmt.Errorf("mysqldump exited with %v: %s", err, msg)
		}
		return fmt.Errorf("mysqldump exited with %v", err)
	}
	return nil
}
