package sandbox

import "testing"

// The server gates work by this, so it must answer from the images rather than
// a list beside them. Node publishes no official Windows image, so npm work on
// a Windows daemon fails before the first stage.
func TestSupportsWindowsAnswersFromTheImages(t *testing.T) {
	for _, eco := range []string{"golang", "pypi"} {
		if !SupportsWindows(eco) {
			t.Errorf("%s has a Windows image but SupportsWindows says no", eco)
		}
	}
	for _, eco := range []string{"npm", "cargo", "gem", "composer", "hex", "pub", "maven", "", "nonsense"} {
		if SupportsWindows(eco) {
			t.Errorf("%s has no Windows image but SupportsWindows says yes", eco)
		}
	}
}
