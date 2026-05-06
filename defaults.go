package goandroidauto

import "embed"

// DefaultFiles contains the automation scripts and CSV examples copied into
// the user's config directory on first run.
//
//go:embed automation/*.yaml automation/*/*.yaml automation/*/*/*.yaml values/*.csv
var DefaultFiles embed.FS
