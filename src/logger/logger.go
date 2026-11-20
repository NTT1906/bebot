package logger

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DailyLogger writes log output to daily rotating files and purges logs older than 7 days.
type DailyLogger struct {
	mu       sync.Mutex
	dir      string
	currDate string
	file     *os.File
}

// NewDailyLogger initializes the daily logger in the specified directory.
func NewDailyLogger(dir string) *DailyLogger {
	_ = os.MkdirAll(dir, 0755)
	l := &DailyLogger{dir: dir}
	go l.cleanupLoop()
	return l
}

func (l *DailyLogger) Write(p []byte) (n int, err error) {
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
			return 0, err
		}
		l.file = f
		l.currDate = dateStr

		go l.cleanup()
	}
	return l.file.Write(p)
}

func (l *DailyLogger) cleanupLoop() {
	for {
		time.Sleep(24 * time.Hour)
		l.cleanup()
	}
}

func (l *DailyLogger) cleanup() {
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -7)
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), "bebot-") && strings.HasSuffix(entry.Name(), ".log") {
			datePart := strings.TrimSuffix(strings.TrimPrefix(entry.Name(), "bebot-"), ".log")
			logDate, err := time.Parse("2006-01-02", datePart)
			if err == nil && logDate.Before(cutoff) {
				_ = os.Remove(filepath.Join(l.dir, entry.Name()))
			}
		}
	}
}
