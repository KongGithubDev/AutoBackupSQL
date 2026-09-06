package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"
)

// parseTimes validates "HH:MM" strings and returns sorted, unique
// minutes-of-day (0..1439).
func parseTimes(raw []string) ([]int, error) {
	set := make(map[int]bool)
	for _, s := range raw {
		var h, m int
		if _, err := fmt.Sscanf(s, "%d:%d", &h, &m); err != nil || s != fmt.Sprintf("%02d:%02d", h, m) {
			return nil, fmt.Errorf("invalid schedule time %q (expected HH:MM, 24h format)", s)
		}
		if h < 0 || h > 23 || m < 0 || m > 59 {
			return nil, fmt.Errorf("invalid schedule time %q (expected HH:MM, 24h format)", s)
		}
		set[h*60+m] = true
	}
	if len(set) == 0 {
		return nil, errors.New("schedule.times must not be empty")
	}
	out := make([]int, 0, len(set))
	for t := range set {
		out = append(out, t)
	}
	sort.Ints(out)
	return out, nil
}

func minutesOfDay(t time.Time) int { return t.Hour()*60 + t.Minute() }

func instantAt(t time.Time, minuteOfDay int) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), minuteOfDay/60, minuteOfDay%60, 0, 0, t.Location())
}

// latestSlotOnOrBefore returns the most recent scheduled instant that is
// <= now, and true if one exists today.
func latestSlotOnOrBefore(now time.Time, times []int) (time.Time, bool) {
	tod := minutesOfDay(now)
	best := -1
	for _, t := range times {
		if t <= tod {
			best = t
		}
	}
	if best < 0 {
		return time.Time{}, false
	}
	return instantAt(now, best), true
}

// nextSlotAfter returns the next scheduled instant strictly after now.
func nextSlotAfter(now time.Time, times []int) time.Time {
	tod := minutesOfDay(now)
	for _, t := range times {
		if t > tod {
			return instantAt(now, t)
		}
	}
	// All of today's slots have passed -> first slot tomorrow.
	tomorrow := now.AddDate(0, 0, 1)
	return instantAt(tomorrow, times[0])
}

// describeNextRuns formats the next n upcoming run times for logging.
func describeNextRuns(from time.Time, times []int, n int) string {
	next := make([]string, 0, n)
	t := from
	for i := 0; i < n; i++ {
		t = nextSlotAfter(t, times)
		next = append(next, t.Format("2006-01-02 15:04"))
	}
	return fmt.Sprintf("%v", next)
}

// state holds the timestamp of the last completed backup, persisted to
// state.json so restarts do not re-run an already covered schedule slot.
type state struct {
	LastRun string `json:"lastRun"`
}

func loadState(path string) (time.Time, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return time.Time{}, nil
		}
		return time.Time{}, err
	}
	var s state
	if err := json.Unmarshal(data, &s); err != nil {
		return time.Time{}, fmt.Errorf("invalid state file %q: %w", path, err)
	}
	if s.LastRun == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, s.LastRun)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid timestamp in state file %q: %w", path, err)
	}
	return t, nil
}

func saveState(path string, t time.Time) error {
	data, err := json.Marshal(state{LastRun: t.UTC().Format(time.RFC3339)})
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
