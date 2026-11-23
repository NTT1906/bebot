package logger

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// DailyLogger writes log output asynchronously to daily rotating files and purges logs older than 7 days.
type DailyLogger struct {
	dir      string
	currDate string
	file     *os.File
	ch       chan []byte
	done     chan struct{}
	closed   atomic.Bool
	wg       sync.WaitGroup
}

// NewDailyLogger initializes the asynchronous daily logger in the specified directory.
func NewDailyLogger(dir string) *DailyLogger {
	_ = os.MkdirAll(dir, 0755)
	l := &DailyLogger{
		dir:  dir,
		ch:   make(chan []byte, 2048),
		done: make(chan struct{}),
	}
	l.wg.Add(1)
	go l.writerLoop()
	go l.cleanupLoop()
	return l
}

// Write queues the log bytes to the asynchronous buffer without blocking the caller.
func (l *DailyLogger) Write(p []byte) (n int, err error) {
	if l.closed.Load() {
		return len(p), nil
	}
	// Copy p to preserve content from caller buffer reuse
	cp := append(make([]byte, 0, len(p)), p...)
	select {
	case l.ch <- cp:
		return len(p), nil
	case <-l.done:
		return len(p), nil
	}
}

// Close gracefully flushes all queued log entries to disk and closes the active log file.
func (l *DailyLogger) Close() error {
	if l.closed.CompareAndSwap(false, true) {
		close(l.done)
		l.wg.Wait()
	}
	return nil
}

func (l *DailyLogger) writerLoop() {
	defer l.wg.Done()
	for {
		select {
		case p, ok := <-l.ch:
			if !ok {
				l.flushRemaining()
				return
			}
			l.writeToFile(p)
		case <-l.done:
			l.flushRemaining()
			return
		}
	}
}

func (l *DailyLogger) flushRemaining() {
	for {
		select {
		case p := <-l.ch:
			l.writeToFile(p)
		default:
			if l.file != nil {
				_ = l.file.Sync()
				_ = l.file.Close()
				l.file = nil
			}
			return
		}
	}
}

func (l *DailyLogger) writeToFile(p []byte) {
	dateStr := time.Now().Format("2006-01-02")
	if l.currDate != dateStr || l.file == nil {
		if l.file != nil {
			_ = l.file.Sync()
			_ = l.file.Close()
		}
		filename := filepath.Join(l.dir, fmt.Sprintf("bebot-%s.log", dateStr))
		f, err := os.OpenFile(filename, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err != nil {
			return
		}
		l.file = f
		l.currDate = dateStr
		go l.cleanup()
	}
	_, _ = l.file.Write(p)
}

func (l *DailyLogger) cleanupLoop() {
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			l.cleanup()
		case <-l.done:
			return
		}
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
