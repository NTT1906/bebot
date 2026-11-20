package protocol

import (
	"fmt"
	"strconv"
	"strings"
)

// ServerPong holds the decoded fields from a RakNet UnconnectedPong response.
type ServerPong struct {
	Edition, MOTD       string
	ProtocolID          int32
	Version             string
	Players, MaxPlayers int32
}

// ParseServerPong parses the raw byte payload from RakNet pong into a ServerPong.
func ParseServerPong(raw []byte) (ServerPong, error) {
	parts := strings.Split(string(raw), ";")
	if len(parts) < 6 {
		return ServerPong{}, fmt.Errorf("invalid RakNet pong: only %d fields", len(parts))
	}
	protocolID, err := strconv.Atoi(parts[2])
	if err != nil {
		return ServerPong{}, fmt.Errorf("invalid server protocol %q", parts[2])
	}
	players, err := strconv.Atoi(parts[4])
	if err != nil {
		return ServerPong{}, err
	}
	maxPlayers, err := strconv.Atoi(parts[5])
	if err != nil {
		return ServerPong{}, err
	}
	return ServerPong{
		Edition:    parts[0],
		MOTD:       parts[1],
		ProtocolID: int32(protocolID),
		Version:    parts[3],
		Players:    int32(players),
		MaxPlayers: int32(maxPlayers),
	}, nil
}
