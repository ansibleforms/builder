package version

import (
	"regexp"
	"testing"
)

// TestAppIsARelease checks App is a release version (x.y.z, no pre-release): the release
// workflow names the release after it and refuses anything else, and the controller reads it
// in the builder's hello.
func TestAppIsARelease(t *testing.T) {
	if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(App) {
		t.Fatalf("App is %q, not a release version (x.y.z)", App)
	}
}
