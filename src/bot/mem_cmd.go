package bot

import (
	"fmt"
	"log"
	"runtime"
	"runtime/debug"
	"strings"
)

// MemStatsSummary holds key metrics from runtime.MemStats.
type MemStatsSummary struct {
	Alloc     uint64
	TotalAlloc uint64
	Sys       uint64
	HeapInuse uint64
	HeapIdle  uint64
	NumGC     uint32
}

// GetMemStats returns a snapshot of the current Go runtime memory stats.
func GetMemStats() MemStatsSummary {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return MemStatsSummary{
		Alloc:      m.Alloc,
		TotalAlloc: m.TotalAlloc,
		Sys:        m.Sys,
		HeapInuse:  m.HeapInuse,
		HeapIdle:   m.HeapIdle,
		NumGC:      m.NumGC,
	}
}

// TrimMemory forces garbage collection and returns unneeded virtual memory back to the OS.
func (b *Bot) TrimMemory() (before, after MemStatsSummary) {
	before = GetMemStats()
	runtime.GC()
	debug.FreeOSMemory()
	after = GetMemStats()

	log.Printf("bebot: memory trim completed (Alloc: %.2f MB -> %.2f MB, Sys: %.2f MB -> %.2f MB, HeapInuse: %.2f MB -> %.2f MB, NumGC: %d)\n",
		float64(before.Alloc)/(1024*1024), float64(after.Alloc)/(1024*1024),
		float64(before.Sys)/(1024*1024), float64(after.Sys)/(1024*1024),
		float64(before.HeapInuse)/(1024*1024), float64(after.HeapInuse)/(1024*1024),
		after.NumGC,
	)
	return before, after
}

// showMemStats replies with current detailed memory stats.
func (b *Bot) showMemStats() {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	allocMB := float64(m.Alloc) / (1024 * 1024)
	sysMB := float64(m.Sys) / (1024 * 1024)
	inUseMB := float64(m.HeapInuse) / (1024 * 1024)
	idleMB := float64(m.HeapIdle) / (1024 * 1024)
	releasedMB := float64(m.HeapReleased) / (1024 * 1024)

	lines := []string{
		"§6--- Bebot Memory Stats ---§r",
		fmt.Sprintf("Heap Alloc: %.2f MB | Sys: %.2f MB", allocMB, sysMB),
		fmt.Sprintf("Heap InUse: %.2f MB | Idle: %.2f MB (Released: %.2f MB)", inUseMB, idleMB, releasedMB),
		fmt.Sprintf("Goroutines: %d | GC Cycles: %d", runtime.NumGoroutine(), m.NumGC),
	}
	b.reply(strings.Join(lines, "\n"))
}

// execTrimMemory executes memory trim and replies with the outcome.
func (b *Bot) execTrimMemory() {
	before, after := b.TrimMemory()
	freedMB := float64(int64(before.Alloc)-int64(after.Alloc)) / (1024 * 1024)
	if freedMB < 0 {
		freedMB = 0
	}

	b.reply(fmt.Sprintf("[Memory] Trim completed: Alloc %.2f MB -> %.2f MB | Sys %.2f MB -> %.2f MB | HeapInuse %.2f MB -> %.2f MB (freed ~%.2f MB heap).",
		float64(before.Alloc)/(1024*1024), float64(after.Alloc)/(1024*1024),
		float64(before.Sys)/(1024*1024), float64(after.Sys)/(1024*1024),
		float64(before.HeapInuse)/(1024*1024), float64(after.HeapInuse)/(1024*1024),
		freedMB,
	))
}

// memCmd handles in-game and console commands: "trim", "gc", "memory", "memtrim".
func (b *Bot) memCmd(args []string) {
	if len(args) > 0 {
		if strings.EqualFold(args[0], "stats") || strings.EqualFold(args[0], "status") || strings.EqualFold(args[0], "info") {
			b.showMemStats()
			return
		}
		if strings.EqualFold(args[0], "trim") {
			b.execTrimMemory()
			return
		}
	}
	b.execTrimMemory()
}
