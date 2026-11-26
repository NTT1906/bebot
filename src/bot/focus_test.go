package bot

import (
	"testing"

	"github.com/google/uuid"
)

func TestMatchesIdentifier(t *testing.T) {
	selfUUID := uuid.New()
	offlineUUID := uuid.New()

	b := &Bot{
		botName:     "MinerBot",
		selfUUID:    selfUUID,
		offlineUUID: offlineUUID,
	}

	tests := []struct {
		name     string
		target   string
		expected bool
	}{
		{"Exact name", "MinerBot", true},
		{"Lower name", "minerbot", true},
		{"Upper name", "MINERBOT", true},
		{"Self UUID", selfUUID.String(), true},
		{"Offline UUID", offlineUUID.String(), true},
		{"Different name", "GuardBot", false},
		{"Different UUID", uuid.New().String(), false},
		{"Empty string", "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := b.matchesIdentifier(tc.target)
			if got != tc.expected {
				t.Errorf("matchesIdentifier(%q) = %v, want %v", tc.target, got, tc.expected)
			}
		})
	}
}

func TestFocusAndUnfocusFlow(t *testing.T) {
	botA := &Bot{
		botName:   "BotA",
		selfUUID:  uuid.New(),
		isFocused: true,
	}
	botB := &Bot{
		botName:   "BotB",
		selfUUID:  uuid.New(),
		isFocused: true,
	}

	// 1. Initial state: both bots accept commands
	if botA.shouldIgnoreCommandDueToFocus("jump") {
		t.Errorf("BotA should accept commands when no focus is active")
	}
	if botB.shouldIgnoreCommandDueToFocus("jump") {
		t.Errorf("BotB should accept commands when no focus is active")
	}

	// 2. Focus BotA: "!b focus BotA"
	botA.focusCmd([]string{"BotA"})
	botB.focusCmd([]string{"BotA"})

	// BotA should be focused
	if !botA.isFocused || !botA.focusActive {
		t.Errorf("BotA should be focused and focusActive")
	}
	if botA.shouldIgnoreCommandDueToFocus("jump") {
		t.Errorf("BotA should NOT ignore commands when it is focused")
	}

	// BotB should NOT be focused
	if botB.isFocused || !botB.focusActive {
		t.Errorf("BotB should NOT be focused, but focusActive should be true")
	}
	// BotB should ignore regular commands
	if !botB.shouldIgnoreCommandDueToFocus("jump") {
		t.Errorf("BotB SHOULD ignore regular commands like 'jump' when unfocused")
	}
	if !botB.shouldIgnoreCommandDueToFocus("move") {
		t.Errorf("BotB SHOULD ignore regular commands like 'move' when unfocused")
	}
	// But BotB MUST allow "focus" and "unfocus"
	if botB.shouldIgnoreCommandDueToFocus("focus") {
		t.Errorf("BotB must NOT ignore 'focus' command even when unfocused")
	}
	if botB.shouldIgnoreCommandDueToFocus("unfocus") {
		t.Errorf("BotB must NOT ignore 'unfocus' command even when unfocused")
	}

	// 3. Console commands always bypass focus
	botB.fromConsole = true
	if botB.shouldIgnoreCommandDueToFocus("jump") {
		t.Errorf("Console commands on BotB should never be ignored")
	}
	botB.fromConsole = false

	// 4. Focus BotB: "!b focus BotB"
	botA.focusCmd([]string{"BotB"})
	botB.focusCmd([]string{"BotB"})

	// Now BotB is focused, BotA is ignored
	if !botB.isFocused {
		t.Errorf("BotB should now be focused")
	}
	if botA.isFocused {
		t.Errorf("BotA should now be unfocused")
	}
	if !botA.shouldIgnoreCommandDueToFocus("jump") {
		t.Errorf("BotA should now ignore regular commands")
	}
	if botB.shouldIgnoreCommandDueToFocus("jump") {
		t.Errorf("BotB should now accept regular commands")
	}

	// 5. Unfocus: "!b unfocus"
	botA.unfocusCmd()
	botB.unfocusCmd()

	if botA.focusActive || !botA.isFocused {
		t.Errorf("BotA should have focusActive=false and isFocused=true after unfocus")
	}
	if botB.focusActive || !botB.isFocused {
		t.Errorf("BotB should have focusActive=false and isFocused=true after unfocus")
	}
	if botA.shouldIgnoreCommandDueToFocus("jump") || botB.shouldIgnoreCommandDueToFocus("jump") {
		t.Errorf("Both bots should accept commands after unfocus")
	}
}

func TestRunAsCommand(t *testing.T) {
	uuidA := uuid.New()
	uuidB := uuid.New()

	botA := &Bot{
		botName:     "BotA",
		selfUUID:    uuidA,
		focusActive: true,
		isFocused:   true, // BotA is focused
	}
	botB := &Bot{
		botName:     "BotB",
		selfUUID:    uuidB,
		focusActive: true,
		isFocused:   false, // BotB is UNFOCUSED!
	}

	// 1. BotB ignores regular commands
	if !botB.shouldIgnoreCommandDueToFocus("spin") {
		t.Errorf("BotB should ignore 'spin' because it is unfocused")
	}

	// 2. But BotB MUST NOT ignore 'run-as', 'runas', or 'as'
	for _, alias := range []string{"run-as", "runas", "as"} {
		if botB.shouldIgnoreCommandDueToFocus(alias) {
			t.Errorf("BotB must not ignore %q even when unfocused", alias)
		}
	}

	// 3. run-as targeting BotB
	// BotA should ignore because target != BotA
	botA.spinActive = false
	botB.spinActive = false

	botA.command("!b run-as BotB spin")
	botB.command("!b run-as BotB spin")

	if botA.spinActive {
		t.Errorf("BotA should NOT execute command when run-as targets BotB")
	}
	if !botB.spinActive {
		t.Errorf("BotB SHOULD execute command when run-as targets BotB, even though it was unfocused!")
	}

	// 4. run-as targeting BotB by UUID
	botB.spinActive = false
	botB.command("!b run-as " + uuidB.String() + " spin")
	if !botB.spinActive {
		t.Errorf("BotB should execute command when targeted by its UUID")
	}

	// 5. run-as targeting non-existent bot
	botB.spinActive = false
	botB.command("!b run-as NonExistentBot spin")
	if botB.spinActive {
		t.Errorf("BotB should NOT execute command when target does not match")
	}
}
