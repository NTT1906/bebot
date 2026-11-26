package bot

import (
	"testing"

	"github.com/google/uuid"
)

func TestFollowCommand(t *testing.T) {
	botUUID := uuid.New()
	b := &Bot{
		botName:  "Bebot",
		selfUUID: botUUID,
		players: map[string]player{
			"arie1906": {
				name:   "Arie1906",
				online: true,
			},
			"steve": {
				name:   "Steve",
				online: false, // offline
			},
		},
	}

	t.Run("Follow sender when no argument provided", func(t *testing.T) {
		b.mode = modeIdle
		b.followTarget = ""
		b.commandSender = "Arie1906"

		b.command("!b follow")

		if b.mode != modeFollowing {
			t.Errorf("expected modeFollowing, got %v", b.mode)
		}
		if b.followTarget != "Arie1906" {
			t.Errorf("expected followTarget 'Arie1906', got %q", b.followTarget)
		}
	})

	t.Run("Follow specific player by name", func(t *testing.T) {
		b.mode = modeIdle
		b.followTarget = ""
		b.commandSender = "OtherSender"

		b.command("!b follow arie1906")

		if b.mode != modeFollowing {
			t.Errorf("expected modeFollowing, got %v", b.mode)
		}
		if b.followTarget != "Arie1906" {
			t.Errorf("expected followTarget 'Arie1906', got %q", b.followTarget)
		}
	})

	t.Run("Reject non-existent player", func(t *testing.T) {
		b.mode = modeIdle
		b.followTarget = ""

		b.command("!b follow FakeGhost_99")

		if b.mode == modeFollowing {
			t.Errorf("should NOT enter modeFollowing for non-existent player")
		}
		if b.followTarget != "" {
			t.Errorf("followTarget should remain empty, got %q", b.followTarget)
		}
	})

	t.Run("Reject offline player", func(t *testing.T) {
		b.mode = modeIdle
		b.followTarget = ""

		b.command("!b follow steve")

		if b.mode == modeFollowing {
			t.Errorf("should NOT follow offline player")
		}
	})

	t.Run("Reject following self", func(t *testing.T) {
		b.mode = modeIdle
		b.followTarget = ""

		b.command("!b follow Bebot")

		if b.mode == modeFollowing {
			t.Errorf("should NOT follow self")
		}
	})

	t.Run("Follow stop clears following mode", func(t *testing.T) {
		b.mode = modeFollowing
		b.followTarget = "Arie1906"

		b.command("!b follow stop")

		if b.mode != modeIdle {
			t.Errorf("expected modeIdle after follow stop, got %v", b.mode)
		}
		if b.followTarget != "" {
			t.Errorf("followTarget should be cleared, got %q", b.followTarget)
		}
	})

	t.Run("Follow without args and without sender fails gracefully", func(t *testing.T) {
		b.mode = modeIdle
		b.followTarget = ""
		b.commandSender = ""

		b.command("!b follow")

		if b.mode == modeFollowing {
			t.Errorf("should NOT follow when sender is empty and no arg provided")
		}
	})
}
