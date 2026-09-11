package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type dailyLogger struct {
	mu       sync.Mutex
	dir      string
	currDate string
	file     *os.File
}

func newDailyLogger(dir string) *dailyLogger {
	os.MkdirAll(dir, 0755)
	l := &dailyLogger{dir: dir}
	go l.cleanupLoop()
	return l
}

func (l *dailyLogger) Write(p []byte) (n int, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	dateStr := time.Now().Format("2006-01-02")
	if l.currDate != dateStr || l.file == nil {
		if l.file != nil {
			l.file.Close()
		}
		filename := filepath.Join(l.dir, fmt.Sprintf("bebot-%s.log", dateStr))
		f, err := os.OpenFile(filename, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err != nil {
			// If we fail to open the file, just return error and don't panic
			return 0, err
		}
		l.file = f
		l.currDate = dateStr
		
		// Run cleanup on every rotation to ensure old logs are deleted promptly
		go l.cleanup()
	}
	return l.file.Write(p)
}

func (l *dailyLogger) cleanupLoop() {
	// Also run cleanup periodically just in case the bot stays open for days without logging anything
	for {
		time.Sleep(24 * time.Hour)
		l.cleanup()
	}
}

func (l *dailyLogger) cleanup() {
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		return
	}
	// 7 days ago
	cutoff := time.Now().AddDate(0, 0, -7)
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), "bebot-") && strings.HasSuffix(entry.Name(), ".log") {
			// Extract date from filename: bebot-YYYY-MM-DD.log
			datePart := strings.TrimSuffix(strings.TrimPrefix(entry.Name(), "bebot-"), ".log")
			logDate, err := time.Parse("2006-01-02", datePart)
			if err == nil && logDate.Before(cutoff) {
				os.Remove(filepath.Join(l.dir, entry.Name()))
			}
		}
	}
}
