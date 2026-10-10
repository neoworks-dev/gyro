// Package crt holds what is left of the C runtime behind the gyro:rt import
// surface: the aborts, the string layout's last step, threads, channels,
// mutexes, futures and `any`, the linear-memory instructions, and the
// printing functions that METALS from a producer other than Gyro calls.
// The standard library itself is Gyro; docs/appendix-e-native-backend.md
// lists what stays here and why.
//
// internal/nasm links against this one file rather than emitting the
// runtime itself.
package crt

import _ "embed"

// Source is rt.c, embedded so `gyro build` can hand it to the C compiler
// without needing the repository on disk.
//
//go:embed src/rt.c
var Source string
