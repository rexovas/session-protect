package version

import "testing"

// The build stamps these via ldflags; the defaults must stay non-empty so
// unstamped dev builds still report something meaningful everywhere.
func TestDefaultsNonEmpty(t *testing.T) {
	for name, value := range map[string]string{
		"Version": Version, "Commit": Commit, "Date": Date, "Channel": Channel,
	} {
		if value == "" {
			t.Errorf("%s default is empty", name)
		}
	}
}
