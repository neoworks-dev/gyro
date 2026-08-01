// Package stdlib bundles the standard library's declaration files.
//
// Each @gyro/<name> module is an ordinary .gyro source with real signatures
// and no bodies. The compiler resolves imports against these, so name
// resolution and later type checking see honest interfaces; implementations
// arrive with the runtime, which does not exist yet.
package stdlib

import (
	"embed"
	"io/fs"
	"strings"
)

//go:embed src/*.gyro
var sources embed.FS

// Source returns the declaration file for the module @gyro/<name>.
func Source(name string) (string, bool) {
	data, err := sources.ReadFile("src/" + name + ".gyro")
	if err != nil {
		return "", false
	}
	return string(data), true
}

// Modules lists every bundled module name, without the @gyro/ prefix.
func Modules() []string {
	entries, err := fs.ReadDir(sources, "src")
	if err != nil {
		return nil
	}

	var names []string
	for _, entry := range entries {
		names = append(names, strings.TrimSuffix(entry.Name(), ".gyro"))
	}
	return names
}
