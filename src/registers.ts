import { labelGenerator } from "./compiler";

class InterferenceScope {
	public symbols: Map<string, Set<string>> = new Map();
	public children: Record<string, InterferenceScope> = {};

	constructor(public parent?: InterferenceScope) {

	}

	add(name: string, set: Set<string>) {
		this.symbols.set(name, set)
	}

	variables(): string[] {
		if (Object.keys(this.children).length === 0) {
			return Array.from(this.symbols.keys())
		}

		return Object.values(this.children).map(child => child.variables()).flat(Infinity) as string[]
	}

	lookup(name: string): Set<string> | null {
		return this.symbols.get(name) ?? this.parent?.lookup(name) ?? null;
	}
}

export class ScopedInterferenceGraph {
	public scope: InterferenceScope = new InterferenceScope();
	public label = labelGenerator("i_");
	

	enterScope(name: string) {
		if (name in this.scope.children) {
			this.scope = this.scope.children[name]
			return;
		}

		const newScope = new InterferenceScope(this.scope)
		this.scope.children[name] = newScope
		this.scope = newScope
	}

	exitScope() {
		this.scope = this.scope.parent as InterferenceScope;
	}

	addVariable(variable: string) {
			if (!this.scope.lookup(variable)) {
				this.scope.add(variable, new Set());
			}
	}

	addIntermediate() {
		const label = this.label.next().value
		this.scope.add(label, new Set())
		return label
	}

	addEdge(var1: string, var2: string) {
			if (this.scope.lookup(var1) && this.scope.lookup(var2)) {
					this.scope.lookup(var1)?.add(var2);
					this.scope.lookup(var2)?.add(var1);
			}
	}

	getNeighbors(variable: string): Set<string> {
			return this.scope.lookup(variable) || new Set();
	}

	getVariables(): string[] {
			return this.scope.variables();
	}
}



export function colorScopedGraph(graph: ScopedInterferenceGraph, availableRegisters: string[]): Record<string, string> {
	const registerMap: Record<string, string> = {};
	const variables = graph.getVariables();

	// Sort variables by degree (most connected first)
	variables.sort((a, b) => graph.getNeighbors(b).size - graph.getNeighbors(a).size);

	for (const variable of variables) {
			const usedRegisters = new Set(
					[...graph.getNeighbors(variable)]
							.map(neighbor => registerMap[neighbor])
							.filter(register => register) // Remove undefined values
			);

			// Assign the first available register that isn’t in use
			for (const reg of availableRegisters) {
					if (!usedRegisters.has(reg)) {
							registerMap[variable] = reg;
							break;
					}
			}

			// Optional: Spill if no registers are available
			if (!registerMap[variable]) {
					registerMap[variable] = "SPILL_TO_MEMORY"; // Could be stack or RAM
			}
	}

	return registerMap;
}
