// Package stdlib bundles the standard library's declaration files.
//
// Each module is an ordinary .gyro source with real signatures and no
// bodies, imported by bare name: `import { println } from "stdlib"`. The
// compiler resolves imports against these, so name resolution and later
// type checking see honest interfaces; implementations arrive with the
// runtime.
package stdlib

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
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

// Dir is a directory holding a copy of every bundled module as name.gyro,
// for an editor to open: the modules live in the compiler, and a jump to a
// declaration needs a file. The copies are written read-only under the
// user's cache, in a directory named by a hash of their contents, so a
// rebuilt compiler never shows the sources of an older one.
func Dir() (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	for _, name := range Modules() {
		source, _ := Source(name)
		hash.Write([]byte(name + "\x00" + source + "\x00"))
	}
	dir := filepath.Join(cache, "gyro", "stdlib", hex.EncodeToString(hash.Sum(nil))[:16])
	if _, err := os.Stat(dir); err == nil {
		return dir, nil
	}

	// Written beside the final directory and renamed into place, so a
	// reader never sees half of it; losing the race to another writer
	// leaves the same files there.
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", err
	}
	staging, err := os.MkdirTemp(filepath.Dir(dir), "staging-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(staging)
	for _, name := range Modules() {
		source, _ := Source(name)
		if err := os.WriteFile(filepath.Join(staging, name+".gyro"), []byte(source), 0o444); err != nil {
			return "", err
		}
	}
	if err := os.Chmod(staging, 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(staging, dir); err != nil {
		if _, statErr := os.Stat(dir); statErr != nil {
			return "", err
		}
	}
	return dir, nil
}

// ModuleAt reports the bundled module a file in Dir is a copy of.
func ModuleAt(path string) (string, bool) {
	dir, err := Dir()
	if err != nil || filepath.Dir(path) != dir {
		return "", false
	}
	name := strings.TrimSuffix(filepath.Base(path), ".gyro")
	if _, ok := Source(name); !ok {
		return "", false
	}
	return name, true
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
