// Package stdlib bundles the standard library's declaration files.
//
// Each module is an ordinary .gyro source with real signatures and no
// bodies, imported by bare name: `import { println } from "stdlib"`. The
// compiler resolves imports against these, so name resolution and later
// type checking see honest interfaces; implementations arrive with the
// runtime.
package stdlib

import (
	"embed"
	"io/fs"
	"strings"
)

//go:embed src/*.gyro
var sources embed.FS

// pathPrefix marks a module identity as bundled. It is the METALS scheme
// for Gyro modules, so a bundled module's path can never be mistaken for a
// file on disk the way a bare specifier could.
const pathPrefix = "gyro:"

// Source returns the declaration file for the bundled module of that name.
func Source(name string) (string, bool) {
	data, err := sources.ReadFile("src/" + name + ".gyro")
	if err != nil {
		return "", false
	}
	return string(data), true
}

// Path is the module identity the loader records for a bundled module.
func Path(name string) string {
	return pathPrefix + name
}

// IsBundled reports whether a module path names a bundled module. The
// backends use it to tell declarations whose bodies the runtime supplies
// from declarations they must lower themselves.
func IsBundled(modulePath string) bool {
	return strings.HasPrefix(modulePath, pathPrefix)
}

// NameOf recovers the module name from a bundled module's path.
func NameOf(modulePath string) (string, bool) {
	return strings.CutPrefix(modulePath, pathPrefix)
}

// Modules lists every bundled module name, which is also its specifier.
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
