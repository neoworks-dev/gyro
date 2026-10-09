package cc

import (
	"fmt"
	"strconv"

	"github.com/gyro-lang/gyro/internal/metals"
)

// Bit-fields.
//
// A bit-field's place is measured in bits, and METALS has no member
// measured in bits — deliberately, because a format whose records were
// bit-addressed would be a format every consumer had to shift. So a run
// of adjacent bit-fields becomes one member of bytes, and each of C's
// members remembers which bits of that run are its own. Reading one is a
// load, a shift and a mask; writing one is a read-modify-write of the
// bytes it touches.
//
// This is the only place the front end computes a layout of its own, and
// it computes exactly the part METALS cannot be asked: the bit cursor.
// Every size and alignment it needs comes from metals.Layout, which is
// where that arithmetic belongs.
//
// The packing rule is x86-64's, and it is what makes the result
// predictable rather than a convention this compiler invented:
//
//   - A bit-field starts at the next free bit.
//   - It may not cross a boundary of its own declared type's size,
//     counted from the start of the struct. `unsigned a:30; unsigned
//     b:10;` therefore puts b at bit 32, and the struct at eight bytes.
//   - An unnamed zero-width one moves the cursor to the next such
//     boundary and reserves nothing.
//   - The struct's alignment takes in every bit-field's declared type,
//     even though what is stored is bytes. That is what the record's own
//     `align` carries: `unsigned char a:1; unsigned b:1;` is one byte of
//     storage inside four bytes of struct, and nothing in the members
//     would otherwise say the four.

// runName names the storage a run of bit-fields is packed into. C's
// identifiers have no `.`, so nothing a translation unit declares can
// collide with one and no C expression can name it — and METALS reads a
// `.` inside a word, so the name still round-trips through the printer.
func runName(n int) string { return fmt.Sprintf("bits.%d", n) }

// hasBitFields reports whether an aggregate needs any of this at all.
// Most do not, and for those the members and the record's are one list.
func hasBitFields(s *ctype) bool {
	for _, f := range s.fields {
		if f.bits != nil {
			return true
		}
	}
	return false
}

// holdsBitFields reports whether a bit-field is anywhere inside a type,
// however deep. It is what decides whether a local's storage has to be
// zeroed before anything is written to it: writing a bit-field means
// reading the bytes it shares with its neighbours and putting its own
// back, and there is no partial store to do it with. The native backends
// zero a slot anyway, so this is what makes the evaluator agree rather
// than refuse a program C allows.
func (l *lowerer) holdsBitFields(typ *ctype) bool {
	switch {
	case typ.isArray():
		return l.holdsBitFields(typ.elem)
	case typ.isMembered():
		merged := l.shapeOf(typ)
		if hasBitFields(merged) {
			return true
		}
		for _, f := range merged.fields {
			if l.holdsBitFields(f.typ) {
				return true
			}
		}
	}
	return false
}

// pack decides what the METALS record or union declares and where each
// of C's members lives inside it. layout answers sizes and alignments,
// and must already know every type this one holds by value — which C
// guarantees, since a member's type has to be complete where it is
// written.
func (l *lowerer) pack(s *ctype, layout *metals.Layout, pos position) {
	if !hasBitFields(s) {
		s.storage = make([]field, len(s.fields))
		copy(s.storage, s.fields)
		for i := range s.storage {
			s.storage[i].unit = i
			s.fields[i].unit = i
		}
		return
	}
	if s.isUnion() {
		l.packUnion(s, pos)
		return
	}

	var storage []field
	// bit is the next free bit, counted from the start of the struct.
	// placed is the byte just past the last member of storage, which is
	// where metals.Layout will put the next one.
	bit, placed, align, runs := 0, 0, 1, 0
	runStart := -1

	closeRun := func() {
		if runStart < 0 {
			return
		}
		placed = (bit + 7) / 8
		storage[len(storage)-1].typ = arrayOf(typeUChar, int64(placed-runStart))
		runStart = -1
	}

	for i := range s.fields {
		f := &s.fields[i]

		if f.bits == nil {
			closeRun()
			// An ordinary member starts at the next byte its own
			// alignment allows, which is exactly where the layout table
			// will put it once the run before it has a size.
			member := f.typ.metalsType()
			at := roundUp(placed, layout.Align(member))
			placed = at + layout.Size(member)
			bit = placed * 8
			f.unit = len(storage)
			storage = append(storage, field{name: f.name, typ: f.typ, unit: len(storage)})
			continue
		}

		unit := width(f.typ) * 8
		if a := width(f.typ); a > align {
			align = a
		}
		if f.bits.width == 0 {
			closeRun()
			bit = roundUp(bit, unit)
			continue
		}
		if runStart < 0 {
			// The run begins where the layout table will place it,
			// which is right after the member before it. Any bits the
			// straddle rule skips become padding inside the run rather
			// than a gap the two of us would have to agree on.
			runStart = placed
			f.unit = len(storage)
			storage = append(storage, field{name: runName(runs), unit: len(storage)})
			runs++
		} else {
			f.unit = len(storage) - 1
		}
		if bit/unit != (bit+f.bits.width-1)/unit {
			bit = roundUp(bit, unit)
		}
		f.bits.offset = bit - runStart*8
		bit += f.bits.width
	}
	closeRun()

	s.storage = storage
	s.align = align
}

// packUnion is the same question with the answer C gives a union: every
// member begins where the union does. So every bit-field in one starts
// at bit zero and they share a single run, sized to the widest of them —
// which is not packing at all, and is what makes them overlap the way
// the union's other members do.
func (l *lowerer) packUnion(s *ctype, pos position) {
	storage := make([]field, 0, len(s.fields))
	align, run, widest := 1, -1, 0

	for i := range s.fields {
		f := &s.fields[i]
		if f.bits == nil {
			f.unit = len(storage)
			storage = append(storage, field{name: f.name, typ: f.typ, unit: f.unit})
			continue
		}
		if f.bits.width == 0 {
			fail(pos, "a zero-width bit-field says where the next one starts, and in a union there is no next one")
		}
		if a := width(f.typ); a > align {
			align = a
		}
		if f.bits.width > widest {
			widest = f.bits.width
		}
		if run < 0 {
			run = len(storage)
			storage = append(storage, field{name: runName(0), unit: run})
		}
		f.unit = run
		f.bits.offset = 0
	}
	if run >= 0 {
		storage[run].typ = arrayOf(typeUChar, int64((widest+7)/8))
	}

	s.storage = storage
	s.align = align
}

func roundUp(value, align int) int {
	if align <= 1 {
		return value
	}
	return (value + align - 1) / align * align
}

// ---------------------------------------------------------------------------
// Reading and writing
// ---------------------------------------------------------------------------
//
// Bit zero of a run is the least significant bit of its first byte,
// which is what x86-64 means by a bit-field. The packing rule keeps a
// bit-field inside one boundary of its declared type, so it never spans
// more than eight bytes and its bits never reach past a word — which is
// what lets both directions be one register's worth of shifting.

// bitSpan is the byte range of a run a bit-field touches, and how far
// into the first of them it begins.
func (f *funcLowerer) bitSpan(bits *bitfield) (first, last, shift int) {
	first = bits.offset / 8
	last = (bits.offset + bits.width - 1) / 8
	return first, last, bits.offset % 8
}

// loadBits reads a bit-field: the bytes it touches, assembled into one
// register, shifted down, masked to its width, and sign-extended when it
// was declared signed. The result has the bit-field's declared type,
// which is what C reads one as.
func (f *funcLowerer) loadBits(lv value) value {
	bits := lv.bits
	first, last, shift := f.bitSpan(bits)
	run := f.temp()
	f.emit(&metals.ElemPtr{Dst: run, Ptr: lv.reg})

	acc := ""
	for at := first; at <= last; at++ {
		raw := f.temp()
		f.emit(&metals.Load{Dst: raw, Ptr: f.elementAt(run, at)})
		wide := f.temp()
		f.emit(&metals.Conv{Dst: wide, X: raw, To: metals.TUint64})
		if at > first {
			moved := f.temp()
			f.emit(&metals.Bin{Dst: moved, Op: "shl", A: wide, B: f.constant(typeULong, strconv.Itoa(8*(at-first)))})
			wide = moved
		}
		if acc == "" {
			acc = wide
			continue
		}
		joined := f.temp()
		f.emit(&metals.Bin{Dst: joined, Op: "bor", A: acc, B: wide})
		acc = joined
	}

	if shift > 0 {
		moved := f.temp()
		f.emit(&metals.Bin{Dst: moved, Op: "shr", A: acc, B: f.constant(typeULong, strconv.Itoa(shift))})
		acc = moved
	}
	// The high bits above the field are its neighbours', except where
	// the field runs to the top of the bytes it touches and there are
	// none.
	if bits.width < 8*(last-first+1)-shift {
		masked := f.temp()
		f.emit(&metals.Bin{Dst: masked, Op: "band", A: acc,
			B: f.constant(typeULong, strconv.FormatUint(widthMask(bits.width), 10))})
		acc = masked
	}

	out := value{reg: acc, typ: typeULong}
	if bits.signed {
		// The stored bits are the value's low ones, so its sign is bit
		// width-1. Shifting it up to the top and back down arithmetically
		// is the extension, and it is one C would have to write too.
		up := f.temp()
		f.emit(&metals.Bin{Dst: up, Op: "shl", A: acc, B: f.constant(typeULong, strconv.Itoa(64-bits.width))})
		asSigned := f.temp()
		f.emit(&metals.Bitcast{Dst: asSigned, X: up, To: metals.TInt64})
		down := f.temp()
		f.emit(&metals.Bin{Dst: down, Op: "shr", A: asSigned, B: f.constant(typeLong, strconv.Itoa(64-bits.width))})
		out = value{reg: down, typ: typeLong}
	}
	return f.convert(out, lv.typ, position{})
}

// storeBits writes a bit-field back: the value masked to its width and
// shifted into place, then merged into each byte of the run it touches.
// A byte the field covers entirely is written outright; one it shares is
// read, cleared where the field sits, and written back.
func (f *funcLowerer) storeBits(lv value, v value) {
	bits := lv.bits
	first, last, shift := f.bitSpan(bits)
	run := f.temp()
	f.emit(&metals.ElemPtr{Dst: run, Ptr: lv.reg})

	// The value arrives as the bit-field's declared type and is used as
	// bits, so it is read unsigned before anything is shifted.
	wide := f.convert(f.convert(v, unsignedOf(promote(lv.typ)), position{}), typeULong, position{})
	trimmed := f.temp()
	f.emit(&metals.Bin{Dst: trimmed, Op: "band", A: wide.reg, B: f.constant(typeULong, strconv.FormatUint(widthMask(bits.width), 10))})
	placed := trimmed
	if shift > 0 {
		moved := f.temp()
		f.emit(&metals.Bin{Dst: moved, Op: "shl", A: trimmed, B: f.constant(typeULong, strconv.Itoa(shift))})
		placed = moved
	}

	mask := widthMask(bits.width) << uint(shift)
	for at := first; at <= last; at++ {
		byteMask := (mask >> uint(8*(at-first))) & 0xFF
		if byteMask == 0 {
			continue
		}
		part := placed
		if at > first {
			moved := f.temp()
			f.emit(&metals.Bin{Dst: moved, Op: "shr", A: placed, B: f.constant(typeULong, strconv.Itoa(8*(at-first)))})
			part = moved
		}

		merged := part
		if byteMask != 0xFF {
			// The byte is shared, so what is not this field's has to
			// survive: clear its bits and put the new ones in.
			mine := f.temp()
			f.emit(&metals.Bin{Dst: mine, Op: "band", A: part, B: f.constant(typeULong, strconv.Itoa(int(byteMask)))})
			old := f.temp()
			f.emit(&metals.Load{Dst: old, Ptr: f.elementAt(run, at)})
			oldWide := f.temp()
			f.emit(&metals.Conv{Dst: oldWide, X: old, To: metals.TUint64})
			kept := f.temp()
			f.emit(&metals.Bin{Dst: kept, Op: "band", A: oldWide, B: f.constant(typeULong, strconv.Itoa(int(^byteMask&0xFF)))})
			joined := f.temp()
			f.emit(&metals.Bin{Dst: joined, Op: "bor", A: kept, B: mine})
			merged = joined
		}
		narrow := f.temp()
		f.emit(&metals.Trunc{Dst: narrow, X: merged, To: metals.TUint8})
		f.emit(&metals.Store{Ptr: f.elementAt(run, at), Val: narrow})
	}
}

// widthMask is the low n bits set. A 64-bit field masks to everything,
// which is the one width `1<<n - 1` cannot spell.
func widthMask(n int) uint64 {
	if n >= 64 {
		return ^uint64(0)
	}
	return 1<<uint(n) - 1
}
