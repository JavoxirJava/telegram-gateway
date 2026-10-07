// Package buildinfo identifies the release and exact source used by the running service.
package buildinfo

import "runtime/debug"

const Version = "2.0.1"

// Revision and BuiltAt are set with linker flags for container release builds.
var Revision = "unknown"
var BuiltAt = "unknown"

func Commit() string {
	if Revision != "unknown" {
		return Revision
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" {
				return setting.Value
			}
		}
	}
	return Revision
}
func Info() map[string]string {
	return map[string]string{"version": Version, "revision": Commit(), "built_at": BuiltAt}
}
