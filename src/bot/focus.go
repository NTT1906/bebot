package bot

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// matchesIdentifier checks if target matches this bot's name or UUID (case-insensitive).
func (b *Bot) matchesIdentifier(target string) bool {
	target = strings.TrimSpace(target)
	if target == "" {
		return false
	}
	if strings.EqualFold(b.botName, target) {
		return true
	}
	if strings.EqualFold(cleanChatName(b.botName), cleanChatName(target)) {
		return true
	}
	if b.selfUUID != uuid.Nil && strings.EqualFold(b.selfUUID.String(), target) {
		return true
	}
	if b.offlineUUID != uuid.Nil && strings.EqualFold(b.offlineUUID.String(), target) {
		return true
	}
	return false
}

// focusCmd handles the 'focus' command.
func (b *Bot) focusCmd(args []string) {
	if len(args) == 0 {
		b.mu.Lock()
		active := b.focusActive
		target := b.focusTarget
		focused := b.isFocused
		b.mu.Unlock()
		if active {
			if focused {
				b.reply(fmt.Sprintf("[%s] Currently focused on me (%s). Use 'unfocus' to clear.", b.botName, target))
			}
		} else {
			b.reply(fmt.Sprintf("[%s] No bot is specifically focused. All bots are accepting commands. Usage: focus <bot name|bot uuid>", b.botName))
		}
		return
	}

	target := strings.Join(args, " ")
	b.mu.Lock()
	match := b.matchesIdentifier(target)
	b.focusActive = true
	b.focusTarget = target
	b.isFocused = match
	b.mu.Unlock()

	if match {
		b.reply(fmt.Sprintf("[%s] Focused! Now accepting commands.", b.botName))
	}
}

// unfocusCmd handles the 'unfocus' command.
func (b *Bot) unfocusCmd() {
	b.mu.Lock()
	wasActive := b.focusActive
	wasFocused := b.isFocused
	b.focusActive = false
	b.focusTarget = ""
	b.isFocused = true
	b.mu.Unlock()

	if wasActive && wasFocused {
		b.reply(fmt.Sprintf("[%s] Unfocused. All bots now accepting commands.", b.botName))
	} else if wasActive {
		b.reply(fmt.Sprintf("[%s] Unfocused.", b.botName))
	} else {
		b.reply(fmt.Sprintf("[%s] Focus was not active.", b.botName))
	}
}

// shouldIgnoreCommandDueToFocus returns true if a command should be dropped because another bot is focused.
func (b *Bot) shouldIgnoreCommandDueToFocus(cmdName string) bool {
	b.mu.Lock()
	fromConsole := b.fromConsole
	focusActive := b.focusActive
	isFocused := b.isFocused
	b.mu.Unlock()

	if fromConsole {
		return false
	}
	if !focusActive {
		return false
	}
	if isFocused {
		return false
	}
	// When not focused, only allow "focus", "unfocus", "run-as", "runas", "as" so targeted commands can be processed.
	switch cmdName {
	case "focus", "unfocus", "run-as", "runas", "as":
		return false
	}
	return true
}

// runAsCmd handles the 'run-as' (or 'runas', 'as') command:
// Syntax: run-as <bot name|bot uuid> <command...>
// Executes the given command as the targeted bot even if focus mode is active on another bot.
func (b *Bot) runAsCmd(args []string) {
	if len(args) < 2 {
		b.mu.Lock()
		focused := b.isFocused || !b.focusActive
		b.mu.Unlock()
		if focused {
			b.reply("Usage: run-as <bot name|bot uuid> <command...>")
		}
		return
	}

	target := args[0]
	if !b.matchesIdentifier(target) {
		return
	}

	subCmd := strings.Join(args[1:], " ")
	b.commandInternal(subCmd, true)
}
