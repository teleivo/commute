// Package version provides build version information.
package version

import "runtime/debug"

// Version returns the module version from embedded build info.
func Version() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	return info.Main.Version
}
