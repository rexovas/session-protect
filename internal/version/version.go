// Package version carries build identity. Release builds stamp these via
// ldflags (GoReleaser or scripts/install.sh); anything else falls back to
// the version Go itself embeds from the VCS state (Go 1.24+ stamps the
// tag, or a pseudo-version/+dirty marker).
package version

import "runtime/debug"

var (
	Version       = "dev"
	Commit        = "unknown"
	Date          = "unknown"
	Channel       = "source"
	SourceDir     = "unknown"
	InstallPrefix = "unknown"
)

func init() {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	if Version == "dev" && info.Main.Version != "" && info.Main.Version != "(devel)" {
		Version = info.Main.Version
	}
	if Commit == "unknown" {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" && len(setting.Value) >= 7 {
				Commit = setting.Value
			}
		}
	}
}
