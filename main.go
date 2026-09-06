package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const version = "1.0.0"

const usageText = `jmdb-backup - MariaDB automatic backup to Cloudflare R2

Usage:
  jmdb-backup [options]

Options:
  -config <file>   Path to config.yaml (default: "config.yaml" in the current dir)
  -once            Run one backup right now, then exit
  -validate        Check config.yaml, locate mysqldump and test R2 access, then exit
  -next            Print the next upcoming backup times, then exit
  -h, -help        Show this help

Without options the program stays in the console and backs up at the
schedule.times configured in config.yaml (default 00:00, 05:00, 12:00, 17:00).
`

type options struct {
	configPath string
	once       bool
	validate   bool
	next       bool
	help       bool
}

func parseArgs(args []string) (options, error) {
	opts := options{configPath: "config.yaml"}
	needValue := func(i *int, name string) (string, error) {
		if *i+1 >= len(args) {
			return "", fmt.Errorf("flag -%s requires a value", name)
		}
		*i++
		return args[*i], nil
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		name := strings.TrimLeft(arg, "-")
		val := ""
		hasVal := false
		if eq := strings.Index(name, "="); eq >= 0 {
			val = name[eq+1:]
			name = name[:eq]
			hasVal = true
		}
		switch name {
		case "config":
			if !hasVal {
				v, err := needValue(&i, name)
				if err != nil {
					return opts, err
				}
				val = v
			}
			opts.configPath = val
		case "once":
			opts.once = true
		case "validate":
			opts.validate = true
		case "next":
			opts.next = true
		case "h", "help":
			opts.help = true
		default:
			return opts, fmt.Errorf("unknown flag: %s", arg)
		}
	}
	return opts, nil
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	opts, err := parseArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "jmdb-backup:", err)
		os.Stderr.WriteString(usageText)
		return pauseOnError(2)
	}
	if opts.help {
		os.Stdout.WriteString(usageText)
		return 0
	}

	cfg, err := LoadConfig(opts.configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "jmdb-backup:", err)
		fmt.Fprintln(os.Stderr, "Copy config.example.yaml to config.yaml and fill in your settings.")
		return pauseOnError(2)
	}

	log, err := NewLogger(cfg.Logging.LogFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "jmdb-backup:", err)
		return pauseOnError(2)
	}
	defer log.Close()

	times, err := parseTimes(cfg.Schedule.Times)
	if err != nil {
		log.Error("%v", err)
		return pauseOnError(2)
	}

	if opts.next {
		fmt.Printf("Config: %s\n", opts.configPath)
		fmt.Printf("Next backups: %s\n", describeNextRuns(time.Now(), times, 6))
		return 0
	}

	store, err := newR2Store(cfg.R2, cfg.Storage.ObjectPrefix)
	if err != nil {
		log.Error("%v", err)
		return pauseOnError(2)
	}

	startedAt := time.Now()

	if opts.validate {
		return runValidate(cfg, store, log, times)
	}
	if opts.once {
		return runOnce(cfg, store, log, startedAt)
	}

	log.Info("jmdb-backup v%s started (config: %s)", version, opts.configPath)
	log.Info("schedule times: %s", strings.Join(cfg.Schedule.Times, ", "))
	log.Info("databases: %s | R2 bucket: %s | prefix: %s | retentionDays: %d",
		strings.Join(cfg.Database.Databases, ", "), cfg.R2.Bucket,
		cfg.Storage.ObjectPrefix, cfg.Storage.RetentionDays)

	// Quick connectivity probe: log a warning but keep running (the next
	// scheduled attempt will retry if this was only a transient problem).
	pingCtx, cancelPing := context.WithTimeout(context.Background(), 15*time.Second)
	if err := store.ping(pingCtx); err != nil {
		log.Error("startup R2 check failed: %v", err)
		log.Error("backups will still be attempted at their scheduled times")
	} else {
		log.Info("R2 bucket %q reachable", cfg.R2.Bucket)
	}
	cancelPing()

	return runScheduler(cfg, store, log, stateFilePath(opts.configPath), startedAt)
}

// runOnce performs a single backup immediately.
func runOnce(cfg *Config, store *r2Store, log *Logger, startedAt time.Time) int {
	b := NewBackuper(cfg, store, log)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Hour)
	defer cancel()
	if _, err := b.RunOnce(ctx, startedAt); err != nil {
		log.Error("%v", err)
		return pauseOnError(1)
	}
	log.Info("run-once finished successfully")
	return 0
}

// runValidate checks everything that can be checked without starting a loop.
func runValidate(cfg *Config, store *r2Store, log *Logger, times []int) int {
	log.Info("validating configuration...")
	failed := false
	fail := func(format string, a ...any) {
		failed = true
		log.Error(format, a...)
	}

	if _, err := findMySQLDump(cfg.Database.MySQLDumpPath); err != nil {
		fail("%v", err)
	} else {
		log.Info("mysqldump: found")
	}
	if strings.Contains(cfg.R2.Endpoint, "ACCOUNT_ID") || cfg.R2.Endpoint == "" {
		fail("cloudflareR2.endpoint must be set to https://<ACCOUNT_ID>.r2.cloudflarestorage.com (replace ACCOUNT_ID with your Cloudflare account id)")
	} else {
		log.Info("R2 endpoint: %s", cfg.R2.Endpoint)
	}
	if cfg.R2.Bucket == "" {
		fail("cloudflareR2.bucket is empty")
	}
	if cfg.R2.AccessKeyID == "" || cfg.R2.SecretAccessKey == "" {
		fail("cloudflareR2.accessKeyId / secretAccessKey are empty (use ${ENV_VAR} placeholders to avoid storing secrets in the file)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := store.ping(ctx); err != nil {
		fail("%v", err)
	} else {
		log.Info("R2 connection + bucket access: OK")
	}

	log.Info("schedule times parsed: %s", describeNextRuns(time.Now(), times, 6))
	if failed {
		log.Info("VALIDATION FAILED - fix the errors above")
		return pauseOnError(1)
	}
	log.Info("VALIDATION PASSED")
	return 0
}

// runScheduler is the resident loop. It backs up at every scheduled slot and
// waits (sleeping) in between. lastRun is persisted to state.json so a
// restart never re-runs a slot that already completed.
func runScheduler(cfg *Config, store *r2Store, log *Logger, statePath string, startedAt time.Time) int {
	b := NewBackuper(cfg, store, log)
	times, _ := parseTimes(cfg.Schedule.Times)

	lastRun, err := loadState(statePath)
	if err != nil {
		log.Error("cannot read state file %q (ignoring): %v", statePath, err)
	}
	if !lastRun.IsZero() {
		log.Info("last successful backup: %s", lastRun.Local().Format("2006-01-02 15:04:05"))
	}

	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// runBackup executes a full backup cycle. state.json is only written for
	// successful runs (so a restart can retry a failed one), while the
	// in-memory lastRun always advances to avoid hot-loop retries.
	runBackup := func(reason string) (bool, time.Time) {
		now := time.Now()
		log.Info("backup run started (%s)", reason)
		runCtx, cancel := context.WithTimeout(sigCtx, 3*time.Hour)
		uploaded, err := b.RunOnce(runCtx, now)
		cancel()
		if err != nil {
			log.Error("backup run failed: %v", err)
			return false, now
		}
		log.Info("backup run finished: %d database(s) uploaded to R2", len(uploaded))
		return true, now
	}

	// Catch-up: if the program was stopped and a scheduled slot was missed,
	// run one backup on startup so a reboot does not lose the day's backup.
	if cfg.Schedule.CatchUpOnStartup {
		check := startedAt.Add(-2 * time.Minute) // started inside the slot minute? let the loop handle it
		if slot, ok := latestSlotOnOrBefore(check, times); ok && lastRun.Before(slot) {
			log.Info("scheduled time %s was missed while the program was stopped -> running catch-up backup",
				slot.Format("2006-01-02 15:04"))
			if ok, at := runBackup("catch-up after restart"); ok {
				saveState(statePath, at)
			}
			lastRun = time.Now()
		}
	}

	for {
		now := time.Now()
		if slot, ok := latestSlotOnOrBefore(now, times); ok && lastRun.Before(slot) {
			ok, at := runBackup("scheduled time " + slot.Format("15:04"))
			lastRun = at
			if ok {
				if err := saveState(statePath, lastRun); err != nil {
					log.Error("cannot save state: %v", err)
				}
			}
			continue
		}
		next := nextSlotAfter(now, times)
		log.Info("next backup at %s", next.Format("2006-01-02 15:04"))
		wait := time.Until(next) + 300*time.Millisecond // small buffer to never wake too early
		if wait <= 0 {
			wait = time.Second
		}
		t := time.NewTimer(wait)
		select {
		case <-sigCtx.Done():
			t.Stop()
			log.Info("shutting down (signal received)")
			return 0
		case <-t.C:
		}
	}
}

// pauseOnError keeps the console window open when the exe is double-clicked,
// so the user can read the error. Exits immediately when output is piped.
func pauseOnError(code int) int {
	if code != 0 && isInteractiveConsole() {
		fmt.Println("\nPress Enter to exit...")
		var buf [1]byte
		os.Stdin.Read(buf[:])
	}
	return code
}

func isInteractiveConsole() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
