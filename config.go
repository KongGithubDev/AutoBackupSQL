package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the editable configuration for the backup tool.
// All values can also be overridden through environment variables using
// ${VAR_NAME} syntax, e.g. password: "${MARIADB_PASSWORD}".
type Config struct {
	Database DatabaseConfig `yaml:"database"`
	Schedule ScheduleConfig `yaml:"schedule"`
	Storage  StorageConfig  `yaml:"storage"`
	R2       R2Config       `yaml:"cloudflareR2"`
	Logging  LoggingConfig  `yaml:"logging"`
}

type DatabaseConfig struct {
	// MySQLDumpPath: full path to mysqldump.exe. Empty = auto-detect
	// (PATH lookup, then common MariaDB/MySQL install directories).
	MySQLDumpPath string `yaml:"mysqldumpPath"`
	Host          string `yaml:"host"`
	Port          int    `yaml:"port"`
	User          string `yaml:"user"`
	// Password is passed to mysqldump through the MYSQL_PWD environment
	// variable (never visible on the command line). Supports ${ENV_VAR}.
	Password string `yaml:"password"`
	// Databases: list of database names to dump.
	Databases []string `yaml:"databases"`
	// ExtraDumpOptions: extra flags forwarded to mysqldump.
	ExtraDumpOptions []string `yaml:"extraDumpOptions"`
}

type ScheduleConfig struct {
	// Times: daily backup times in 24h "HH:MM" local time.
	Times []string `yaml:"times"`
	// CatchUpOnStartup: run one backup at startup if a scheduled time was
	// missed while the program was not running.
	CatchUpOnStartup bool `yaml:"catchUpOnStartup"`
}

type StorageConfig struct {
	// Compression: "gzip" (default) or "none".
	Compression string `yaml:"compression"`
	// ObjectPrefix: R2 folder that receives the backups
	// (objects: <prefix>/<database>/<timestamp>.sql[.gz]).
	ObjectPrefix string `yaml:"objectPrefix"`
	// RetentionDays: delete R2 objects older than this many days.
	// 0 = keep forever. Only files ending in .sql / .sql.gz under
	// ObjectPrefix are considered.
	RetentionDays int `yaml:"retentionDays"`
}

type R2Config struct {
	// Endpoint: https://<ACCOUNT_ID>.r2.cloudflarestorage.com
	Endpoint      string `yaml:"endpoint"`
	AccessKeyID   string `yaml:"accessKeyId"`
	SecretAccessKey string `yaml:"secretAccessKey"`
	Bucket        string `yaml:"bucket"`
	// Region: R2 accepts "auto".
	Region string `yaml:"region"`
}

type LoggingConfig struct {
	// LogFile: append logs to this file. Empty = console only.
	LogFile string `yaml:"logFile"`
}

// defaultConfig returns defaults so that a minimal config file still works.
func defaultConfig() *Config {
	return &Config{
		Database: DatabaseConfig{
			MySQLDumpPath: "",
			Host:          "127.0.0.1",
			Port:          3306,
			User:          "root",
			Password:      "",
			Databases:     []string{"jmdatabase"},
			ExtraDumpOptions: []string{
				"--single-transaction",
				"--routines",
				"--triggers",
				"--events",
				"--hex-blob",
			},
		},
		Schedule: ScheduleConfig{
			Times:            []string{"00:00", "05:00", "12:00", "17:00"},
			CatchUpOnStartup: true,
		},
		Storage: StorageConfig{
			Compression:   "gzip",
			ObjectPrefix:  "backup",
			RetentionDays: 30,
		},
		R2: R2Config{
			Endpoint: "",
			Region:   "auto",
		},
		Logging: LoggingConfig{LogFile: ""},
	}
}

// LoadConfig reads, expands environment variables and normalizes the config.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read config %q: %w", path, err)
	}
	cfg := defaultConfig()
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("invalid YAML in %q: %w", path, err)
	}
	if err := cfg.expandEnv(); err != nil {
		return nil, err
	}
	if err := cfg.normalize(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// expandEnv replaces ${VAR} / $VAR placeholders in every config string.
func (c *Config) expandEnv() error {
	expand := func(s *string) { *s = strings.TrimSpace(os.ExpandEnv(strings.TrimSpace(*s))) }
	expand(&c.Database.MySQLDumpPath)
	expand(&c.Database.Host)
	expand(&c.Database.User)
	expand(&c.Database.Password)
	for i := range c.Database.Databases {
		expand(&c.Database.Databases[i])
	}
	for i := range c.Database.ExtraDumpOptions {
		expand(&c.Database.ExtraDumpOptions[i])
	}
	for i := range c.Schedule.Times {
		expand(&c.Schedule.Times[i])
	}
	expand(&c.Storage.Compression)
	expand(&c.Storage.ObjectPrefix)
	expand(&c.R2.Endpoint)
	expand(&c.R2.AccessKeyID)
	expand(&c.R2.SecretAccessKey)
	expand(&c.R2.Bucket)
	expand(&c.R2.Region)
	expand(&c.Logging.LogFile)
	return nil
}

// normalize fills defaults and validates the configuration.
func (c *Config) normalize() error {
	// Database section.
	if c.Database.Host == "" {
		c.Database.Host = "127.0.0.1"
	}
	if c.Database.Port == 0 {
		c.Database.Port = 3306
	}
	if c.Database.User == "" {
		c.Database.User = "root"
	}
	if len(c.Database.Databases) == 0 {
		return fmt.Errorf("database.databases is empty: add at least one database (e.g. jmdatabase)")
	}
	for _, db := range c.Database.Databases {
		if strings.TrimSpace(db) == "" {
			return fmt.Errorf("database.databases contains an empty name")
		}
	}

	// Schedule.
	if len(c.Schedule.Times) == 0 {
		return fmt.Errorf("schedule.times is empty: add backup times in HH:MM 24h format")
	}
	if _, err := parseTimes(c.Schedule.Times); err != nil {
		return err
	}

	// Storage.
	c.Storage.Compression = strings.ToLower(c.Storage.Compression)
	if c.Storage.Compression == "" {
		c.Storage.Compression = "gzip"
	}
	if c.Storage.Compression != "gzip" && c.Storage.Compression != "none" {
		return fmt.Errorf("storage.compression must be \"gzip\" or \"none\", got %q", c.Storage.Compression)
	}
	if c.Storage.RetentionDays < 0 {
		c.Storage.RetentionDays = 0
	}
	prefix := strings.ReplaceAll(c.Storage.ObjectPrefix, "\\", "/")
	prefix = strings.Trim(prefix, "/")
	if prefix == "" || prefix == "." {
		prefix = "backup"
	}
	c.Storage.ObjectPrefix = prefix

	// R2.
	if c.R2.Region == "" {
		c.R2.Region = "auto"
	}
	return nil
}

// stateFilePath returns the path of the small state file next to the config.
func stateFilePath(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "state.json")
}
