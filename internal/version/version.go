// Package version provides build version information.
package version

import "fmt"

var (
	commit    = "unknown"
	buildTime = "unknown"
)

// Version returns the build version.
func Version() string {
	return fmt.Sprintf("commit=%s buildTime=%s", commit, buildTime)
}
