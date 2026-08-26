// Package crt holds the C runtime that implements the gyro:rt import
// surface: Go-compatible formatting, strings, arrays, hashing, channels,
// mutexes and futures behind a plain C ABI.
//
// internal/nasm links against this one file rather than emitting the
// runtime itself, so what a program does at runtime is written in C once
// and read as C. Its contract (layouts, message texts, formatting rules)
// is docs/appendix-e-native-backend.md.
package crt

import _ "embed"

// Source is rt.c, embedded so `gyro build` can hand it to the C compiler
// without needing the repository on disk.
//
//go:embed src/rt.c
var Source string
