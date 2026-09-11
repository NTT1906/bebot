# Bebot

Bebot is a Minecraft Bedrock bot written in Go using the `gophertunnel` framework.

## Requirements

- [Go](https://golang.org/dl/) 1.21 or higher

## Configuration

Before running the bot, configure the `config.json` file in the project root. If the file doesn't exist, create it based on this structure:

```json
{
  "_comment": "Edit this file to configure the bot.",
  "superuser": "YourUsername",
  "_comment_superuser": "Only this player can control the bot via chat commands.",
  "server": {
    "host": "your.server.ip",
    "port": 19132,
    "version": "1.20.0"
  },
  "skin": {
    "type": "slim",
    "path": "skin/skin.png",
    "skin": "0",
    "override": false
  },
  "target": {
    "x": 0,
    "y": 64,
    "z": 0
  },
  "behaviour": {
    "reconnectBaseDelayMs": 5000,
    "afkJumpIntervalMs": 60000,
    "walkSpeed": 0.15,
    "renderDistance": 5
  },
  "commands_via_chat": false
}
```

- **`superuser`**: Your Minecraft username. Only you will be able to issue commands to the bot.
- **`server`**: Set the `host`, `port`, and expected bedrock `version` of the server.
- **`skin`**: Set a custom skin. You can define the type (`slim`, `normal`) and point to a local `.png` or folder path.
- **`behaviour`**: Tweaks for how the bot operates in-game (like how often to jump to prevent AFK kicks).

## Usage

1. Clone or download this repository.
2. Open a terminal in the project directory.
3. Configure your `config.json`.
4. Run the bot directly using Go:

```bash
go run .
```

Alternatively, build the binary:

```bash
go build -o bebot.exe .
./bebot.exe
```

When you first run the bot, it will prompt you for Microsoft Live / Xbox authentication. Follow the OAuth link provided in the console to authorize the bot's account.

Once connected, join the server on the same server to see the bot. If you are the `superuser`, you can issue commands directly to it in-game via chat!
