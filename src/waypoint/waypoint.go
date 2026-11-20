package waypoint

import (
	"encoding/json"
	"log"
	"os"
)

// Waypoint represents a saved 3D position with an optional note.
type Waypoint struct {
	X, Y, Z float32
	Note    string
}

// DefaultWaypointsFile is the default filename used for persisting waypoints.
const DefaultWaypointsFile = "waypoints.json"

// LoadWaypoints reads waypoints from a JSON file.
func LoadWaypoints(path string) map[string]Waypoint {
	data, err := os.ReadFile(path)
	if err != nil {
		return make(map[string]Waypoint)
	}
	var wps map[string]Waypoint
	if err := json.Unmarshal(data, &wps); err != nil {
		log.Println("bebot: ignoring invalid waypoints file:", err)
		return make(map[string]Waypoint)
	}
	if wps == nil {
		wps = make(map[string]Waypoint)
	}
	return wps
}

// SaveWaypoints writes waypoints to a JSON file.
func SaveWaypoints(path string, wps map[string]Waypoint) error {
	data, err := json.MarshalIndent(wps, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}
