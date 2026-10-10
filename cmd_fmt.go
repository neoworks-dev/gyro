package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/gyro-lang/gyro/internal/diag"
	"github.com/gyro-lang/gyro/internal/format"
)

// runFmt formats Gyro files in place, or with --check only lists the files
// that formatting would change.
func runFmt(args []string) int {
	check := false
	if len(args) > 0 && args[0] == "--check" {
		check, args = true, args[1:]
	}
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}

	var files []string
	for _, arg := range args {
		found, err := gyroFiles(arg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "gyro: %v\n", err)
			return 2
		}
		files = append(files, found...)
	}

	status := 0
	for _, path := range files {
		source, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "gyro: %v\n", err)
			return 2
		}

		var bag diag.Bag
		formatted, err := format.Source(path, string(source), &bag)
		if err != nil {
			if errors.Is(err, format.ErrSyntax) {
				fmt.Fprint(os.Stderr, bag.Render(diag.AutoStyle(os.Stderr)))
			} else {
				fmt.Fprintf(os.Stderr, "gyro: %v\n", err)
			}
			status = 1
			continue
		}
		if formatted == string(source) {
			continue
		}

		if check {
			fmt.Println(path)
			status = 1
			continue
		}
		if err := os.WriteFile(path, []byte(formatted), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "gyro: %v\n", err)
			return 2
		}
	}
	return status
}

// gyroFiles expands a path argument: a file is itself and a directory is
// every .gyro file under it.
func gyroFiles(path string) ([]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return []string{path}, nil
	}

	var files []string
	err = filepath.WalkDir(path, func(file string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(file, ".gyro") {
			files = append(files, file)
		}
		return nil
	})
	return files, err
}
