// Package buildinfo owns the product identity exposed by every transport.
// Release builds override Commit and BuildDate with -ldflags; Version remains a
// sensible source-build default so every surface reports the same identity.
package buildinfo

const Name = "mcp-manager"

var (
	Version   = "0.4.6-dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)
