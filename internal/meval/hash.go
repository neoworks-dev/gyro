package meval

import "math"

// The scalar half of derived hashing. The composites are folded by the
// functions the lowering synthesizes, so all this answers is the
// gyro:rt import behind hash<i32>, hash<f64>, hash<str> and their kin —
// with the same numbers the interpreter produces, since that is what the
// two are diffed against.

const (
	fnvOffset uint64 = 14695981039346656037
	fnvPrime  uint64 = 1099511628211
)

// hashWord is the splitmix64 finalizer.
func hashWord(x uint64) uint64 {
	x ^= x >> 30
	x *= 0xbf58476d1ce4e5b9
	x ^= x >> 27
	x *= 0x94d049bb133111eb
	x ^= x >> 31
	return x
}

func hashOf(m *machine, v value) uint64 {
	switch v := v.(type) {
	case intVal:
		return hashWord(uint64(v.v))
	case uintVal:
		return hashWord(v.v)
	case floatVal:
		// -0.0 == 0.0, so the two have to hash alike.
		if v.v == 0 {
			return hashWord(math.Float64bits(0))
		}
		return hashWord(math.Float64bits(v.v))
	case boolVal:
		if v {
			return hashWord(1)
		}
		return hashWord(0)
	case strVal:
		h := fnvOffset
		for i := 0; i < len(v); i++ {
			h = (h ^ uint64(v[i])) * fnvPrime
		}
		return h
	}
	m.throw("hash of a value that has none")
	return 0
}
