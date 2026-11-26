package bot

import (
	"fmt"
	"strconv"
	"strings"
)

// spinCmd handles the spin command.
// Syntax: spin [clockwise|counterclockwise|reverse|stop|off] [speed]
// Defaults: clockwise, speed = 10 degrees/tick (~200 deg/s).
func (b *Bot) spinCmd(args []string) {
	if len(args) > 0 {
		arg0 := strings.ToLower(args[0])
		if arg0 == "stop" || arg0 == "off" {
			b.mu.Lock()
			b.spinActive = false
			b.mu.Unlock()
			b.reply("Spin stopped.")
			return
		}
	}

	dir := float32(1.0)
	dirName := "clockwise"
	speed := float32(10.0)

	for _, arg := range args {
		lower := strings.ToLower(arg)
		switch lower {
		case "clockwise", "cw":
			dir = 1.0
			dirName = "clockwise"
		case "counterclockwise", "ccw", "reverse", "rev":
			dir = -1.0
			dirName = "counterclockwise"
		default:
			if val, err := strconv.ParseFloat(arg, 32); err == nil {
				fVal := float32(val)
				if fVal < 0 {
					dir = -1.0
					dirName = "counterclockwise"
					fVal = -fVal
				}
				if fVal > 0 {
					speed = fVal
				}
			}
		}
	}

	if speed > 180 {
		speed = 180
	}
	if speed < 0.5 {
		speed = 0.5
	}

	b.mu.Lock()
	b.spinActive = true
	b.spinDirection = dir
	b.spinSpeed = speed
	b.mu.Unlock()

	b.reply(fmt.Sprintf("Spinning %s at %.1f deg/tick (%.0f deg/s).", dirName, speed, speed*20))
}
