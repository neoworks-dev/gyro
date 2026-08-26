// Package crt holds the C runtime that implements the gyro:rt import
// surface: Go-compatible formatting, strings, arrays, hashing, channels,
// mutexes and futures behind a plain C ABI.
//
// Both native backends link against this one file — internal/nasm, which
// emits x86-64 assembly, and internal/cgen, which emits C — so the
// runtime lives here rather than inside either of them. Its contract
// (layouts, message texts, formatting rules) is docs/appendix-e-native-backends.md.
package crt

import _ "embed"

// Source is rt.c, embedded so `gyro build` can hand it to the C compiler
// without needing the repository on disk.
//
//go:embed src/rt.c
var Source string
