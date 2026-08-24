package interp

import (
	"math"

	"github.com/gyro-lang/gyro/internal/types"
)

// Derived hashing. hash() is part of the built-in method surface wherever
// == is derived, and it is derived the same way: field-wise, so two values
// that compare equal always hash equal.
//
// The algorithm is written out rather than borrowed from a library,
// because all three tiers have to agree on the number byte for byte. A
// scalar goes through the splitmix64 finalizer; a string is FNV-1a over
// its bytes; a composite folds its parts with FNV's prime. The other two
// tiers reach the same numbers through gyro:rt and the functions the
// lowering synthesizes.

const (
	fnvOffset uint64 = 14695981039346656037
	fnvPrime  uint64 = 1099511628211
)

// hashWord is the splitmix64 finalizer: an avalanche over one word, so
// small integers do not land in neighbouring buckets.
func hashWord(x uint64) uint64 {
	x ^= x >> 30
	x *= 0xbf58476d1ce4e5b9
	x ^= x >> 27
	x *= 0x94d049bb133111eb
	x ^= x >> 31
	return x
}

func hashString(text string) uint64 {
	h := fnvOffset
	for i := 0; i < len(text); i++ {
		h = (h ^ uint64(text[i])) * fnvPrime
	}
	return h
}

// hashFloat folds the two zeroes together before taking the bits, since
// -0.0 == 0.0 and equal values must hash equally.
func hashFloat(v float64) uint64 {
	if v == 0 {
		return hashWord(math.Float64bits(0))
	}
	return hashWord(math.Float64bits(v))
}

func hashCombine(h, part uint64) uint64 { return (h ^ part) * fnvPrime }

// hashValue is the derived hash of a value, or false when the value holds
// nothing hashable. Sema has already refused those, so the flag only
// guards against a shape the checker could not see.
func (f *frame) hashValue(v Value) (uint64, bool) {
	switch v := v.(type) {
	case IntVal:
		return hashWord(uint64(v.V)), true
	case UintVal:
		return hashWord(v.V), true
	case FloatVal:
		return hashFloat(v.V), true
	case BoolVal:
		if v {
			return hashWord(1), true
		}
		return hashWord(0), true
	case StringVal:
		return hashString(string(v)), true
	case TupleVal:
		return f.hashParts(fnvOffset, v)
	case StructVal:
		return f.hashParts(fnvOffset, v.Fields)
	case EnumVal:
		h := hashCombine(fnvOffset, hashWord(uint64(v.Def.variantIndex(v.Variant))))
		return f.hashParts(h, v.Payload)
	}
	return 0, false
}

func (f *frame) hashParts(h uint64, parts []Value) (uint64, bool) {
	for _, part := range parts {
		one, ok := f.hashValue(part)
		if !ok {
			return 0, false
		}
		h = hashCombine(h, one)
	}
	return h, true
}

// hashThunk answers a hash() call with the derived hash. A type that
// declares its own hash never reaches here: the declared method resolves
// first, the way to_string does.
func (f *frame) hashThunk(v Value, offset int) func() Value {
	return func() Value {
		h, ok := f.hashValue(v)
		if !ok {
			f.in.panicf(f.module, offset, "%s has no method hash", f.in.repr(v))
		}
		return UintVal{V: h, Kind: types.U64}
	}
}

// isPrimitive reports whether a value inhabits a built-in scalar type,
// whose method surface the compiler owns.
func isPrimitive(v Value) bool {
	switch v.(type) {
	case IntVal, UintVal, FloatVal, BoolVal, StringVal:
		return true
	}
	return false
}

func (d *typeDef) variantIndex(name string) int {
	if d.enumDecl == nil {
		return 0
	}
	for index, variant := range d.enumDecl.Variants {
		if variant.Name == name {
			return index
		}
	}
	return 0
}
