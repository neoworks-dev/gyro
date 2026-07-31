type SymbolKind = "variable" | "function" | "type" | "constant" | "struct";

interface SymbolEntry {
  name: string;
  kind: SymbolKind;
  type: string;
  offset: number; // Memory offset from base register
  metadata?: Record<string, any>;
	size: number;
}

export interface FunctionEntry extends SymbolEntry {
  kind: "function";
  parameters: { name: string; type: string }[];
  returnType: string;
  localScope: Scope; // Each function has its own nested scope
}

export interface StructSymbol extends SymbolEntry {
	kind: "struct";
	fields: { name: string, type: string }[];
}




export class Scope {
  private symbols: Map<string, SymbolEntry> = new Map();
	private stackPointerOffset = 0;
  constructor(public parent: Scope | null = null) {}

	addVariable(name: string, type: string) {
		const size = getTypeSize(type, this)
		const offset = this.stackPointerOffset -= size;
		this.symbols.set(name, { name, kind: "variable", type, offset, size })
		return offset
	}

	addConstant(name: string, type: string) {
		const size = getTypeSize(type, this)
		const offset = this.stackPointerOffset -= size;
		this.symbols.set(name, { name, kind: "constant", type, offset, size })
		return offset
	}

	addFunction(name: string, returnType: string, parameters: { name: string, type: string }[]) {
		const localScope = new Scope(this)
		this.symbols.set(name, { kind: "function", name, returnType, localScope, parameters } as FunctionEntry)

		let paramOffset = 4
		for (const param of parameters) {
			const size = getTypeSize(param.type, this)
			localScope.symbols.set(param.name, { kind: "variable", name: param.name, offset: paramOffset += size, size, type: param.type })
		}

		return localScope
	}

	addStruct(name: string, fields: {name: string, type: string}[]) {
		const size = fields.reduce((acc, field) => {
			return acc + getTypeSize(field.type, this)
		}, 0)
		this.symbols.set(name, { name, kind: "struct", type: name, fields, size } as StructSymbol)
	}

  lookup(name: string): SymbolEntry | null {
    return this.symbols.get(name) ?? this.parent?.lookup(name) ?? null;
  }
}

export function getSizeDescriptor(size: number) {
	if (size === 1) return "byte";
	if (size === 2) return "word";
	if (size === 4) return "dword";
	if (size === 8) return "qword";
	if (size === 16) return "oword"; // Often used for SIMD (128-bit)
	// if (size === 32) return "dword x 2"; // AVX 256-bit
	// if (size === 64) return "zmmword"; // AVX-512 512-bit
}

export function getTypeSize(type: string, scope: Scope): number {
	const typeSizes: Record<string, number> = {
		i8: 1,
		u8: 1,
		i16: 2,
		u16: 2,
		i32: 4,
		u32: 4,
		int: 4, // Typically 32-bit on most platforms
		i64: 8,
		u64: 8,
		f32: 4,
		f64: 8,
		bool: 1, // Often stored as a byte
		char: 1, // Typically 1 byte (UTF-8/ASCII), but may vary
		void: 0, // No size, used for pointers
		pointer: 4, // 4 bytes on 32-bit, 8 bytes on 64-bit systems
		string: 4, // Typically stored as a pointer
	};

	const struct = scope.lookup(type) as StructSymbol
	if (struct?.kind === "struct") {
		return struct.fields.reduce((acc, field) => {
			return acc + getTypeSize(field.type, scope)
		}, 0)
	}

	return typeSizes[type] ?? 4; // Default size for unknown types
}