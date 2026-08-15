package main

import (
	"fmt"
	"strconv"
	"strings"
)

type serverPong struct {
	Edition, MOTD       string
	ProtocolID          int32
	Version             string
	Players, MaxPlayers int32
}

func parseServerPong(raw []byte) (serverPong, error) {
	parts := strings.Split(string(raw), ";")
	if len(parts) < 6 {
		return serverPong{}, fmt.Errorf("invalid RakNet pong: only %d fields", len(parts))
	}
	protocolID, err := strconv.Atoi(parts[2])
	if err != nil {
		return serverPong{}, fmt.Errorf("invalid server protocol %q", parts[2])
	}
	players, err := strconv.Atoi(parts[4])
	if err != nil {
		return serverPong{}, err
	}
	maxPlayers, err := strconv.Atoi(parts[5])
	if err != nil {
		return serverPong{}, err
	}
	return serverPong{Edition: parts[0], MOTD: parts[1], ProtocolID: int32(protocolID), Version: parts[3], Players: int32(players), MaxPlayers: int32(maxPlayers)}, nil
}
