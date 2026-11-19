package types

// Owning reports whether something has to happen when a value of t goes
// away: the type implements Destroy, or one of its fields, elements or
// payloads is owning. Every other type is plain and copies.
//
// A type parameter is plain. An owning type cannot be a type argument
// yet, so nothing a generic body copies can be owning.
func Owning(t Type) bool {
	return owning(t, map[Type]bool{})
}

func owning(t Type, seen map[Type]bool) bool {
	if seen[t] {
		// A type that reaches itself is owning through some other part if
		// at all; the cycle adds nothing.
		return false
	}
	seen[t] = true
	switch t := t.(type) {
	case *Struct:
		if Destroys(t) {
			return true
		}
		for _, field := range t.Fields {
			if owning(field.Type, seen) {
				return true
			}
		}
	case *Enum:
		if Destroys(t) {
			return true
		}
		for _, variant := range t.Variants {
			for _, payload := range variant.Payload {
				if owning(payload, seen) {
					return true
				}
			}
		}
	case *Tuple:
		for _, elem := range t.Elems {
			if owning(elem, seen) {
				return true
			}
		}
	case *Array:
		return owning(t.Elem, seen)
	case *Optional:
		return owning(t.Elem, seen)
	}
	return false
}

// Destroys reports whether the type implements Destroy itself. A generic
// instance answers with its declaration's impls.
func Destroys(t Type) bool {
	var impls []*Impl
	switch t := t.(type) {
	case *Struct:
		if t.Origin != nil {
			t = t.Origin
		}
		impls = t.Impls
	case *Enum:
		if t.Origin != nil {
			t = t.Origin
		}
		impls = t.Impls
	}
	for _, impl := range impls {
		if impl.Trait != nil && impl.Trait.Kind == "Destroy" {
			return true
		}
	}
	return false
}
