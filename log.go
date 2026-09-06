package main

import (
	"fmt"
	"os"
	"sync"
	"time"
)

// Logger writes timestamped lines to stdout and optionally to a log file.
type Logger struct {
	mu   sync.Mutex
	file *os.File
}

func NewLogger(logFile string) (*Logger, error) {
	l := &Logger{}
	if logFile != "" {
		f, err := os.OpenFile(logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return nil, fmt.Errorf("cannot open log file %q: %w", logFile, err)
		}
		l.file = f
	}
	return l, nil
}

func (l *Logger) logf(level, format string, args ...any) {
	line := fmt.Sprintf("[%s] %-5s %s", time.Now().Format("2006-01-02 15:04:05"), level,
		fmt.Sprintf(format, args...))
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Println(line)
	if l.file != nil {
		fmt.Fprintln(l.file, line)
	}
}

func (l *Logger) Info(format string, args ...any)  { l.logf("INFO", format, args...) }
func (l *Logger) Error(format string, args ...any) { l.logf("ERROR", format, args...) }

func (l *Logger) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		l.file.Close()
		l.file = nil
	}
}
