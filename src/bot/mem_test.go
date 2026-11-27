package bot

import (
	"strings"
	"testing"
)

func TestTrimMemory(t *testing.T) {
	b := &Bot{}
	before, after := b.TrimMemory()

	if before.Sys == 0 || after.Sys == 0 {
		t.Errorf("expected non-zero Sys memory stats, got before=%d after=%d", before.Sys, after.Sys)
	}
	if after.NumGC < before.NumGC {
		t.Errorf("expected NumGC to not decrease, before=%d after=%d", before.NumGC, after.NumGC)
	}
}

func TestMemCmd(t *testing.T) {
	b := &Bot{}
	replies := make([]string, 0)
	b.testReplyHook = func(msg string) {
		replies = append(replies, msg)
	}

	// 1. Stats command
	b.memCmd([]string{"stats"})
	if len(replies) == 0 || !strings.Contains(replies[len(replies)-1], "Heap Alloc:") {
		t.Fatalf("expected memory stats reply, got %v", replies)
	}

	// 2. Trim command
	b.memCmd(nil)
	if len(replies) == 0 || !strings.Contains(replies[len(replies)-1], "[Memory] Trim completed:") {
		t.Fatalf("expected memory trim reply, got %v", replies)
	}
}
