package web

import (
	"strings"
	"testing"
)

// A project bucket is a DIRECTORY, not a person. With one peer on the
// network every bucket is the same machine, so "73 Projects this month"
// was one operator's folder count wearing the clothes of a participation
// statistic — and a reader asked whether it meant seventy-three people.
//
// The peer tile is already hidden for exactly this reason. This number
// needed the same rule, and both come back on their own as soon as a
// second peer makes them mean what they say.
func TestParticipationNumbersAreHiddenUntilTheyMeanSomething(t *testing.T) {
	alone := buildTiles("en", &netStats{Peers: 1, ProjectsMonth: 73, Packages: 10})
	for _, tile := range alone {
		if strings.Contains(tile.Label, "Projects this month") {
			t.Errorf("projects tile shown with one peer: %q", tile.Value)
		}
		if strings.Contains(tile.Label, "Peers today") {
			t.Errorf("peers tile shown with one peer: %q", tile.Value)
		}
	}

	joined := buildTiles("en", &netStats{Peers: 2, ProjectsMonth: 73, Packages: 10})
	var sawProjects, sawPeers bool
	for _, tile := range joined {
		if strings.Contains(tile.Label, "Projects this month") {
			sawProjects = true
		}
		if strings.Contains(tile.Label, "Peers today") {
			sawPeers = true
		}
	}
	if !sawProjects || !sawPeers {
		t.Errorf("with a second peer both numbers should return: projects=%v peers=%v",
			sawProjects, sawPeers)
	}
}
