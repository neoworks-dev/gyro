// Compiles the generated ast to assembly

import type { TypeNode } from "typescript";
import { parse } from "./gyro-parser-v1";
import type { BaseNode, ParameterNode, FunctionDeclarationNode, ProgramNode, BlockStatementNode, CallExpressionNode, DecoratorNode, IdentifierNode, LiteralNode, ReturnStatementNode, VariableDeclarationNode, IfStatementNode, BinaryExpressionNode, VariableDeclaratorNode, VisitorPattern } from "./types";
import { getSizeDescriptor, getTypeSize, type FunctionEntry, Scope } from "./scope";
import { colorScopedGraph, ScopedInterferenceGraph } from "./registers";


function compile(input: string) {
	const ast = parse(input);

	const graphVisitor = new InterferenceGraphVisitor();
	graphVisitor.accept(ast)
	console.log(graphVisitor.graph, colorScopedGraph(graphVisitor.graph, ["eax", "ebx", "ecx", "edx"]))

	const visitor = new AssemblyGeneratingVisitor(graphVisitor.graph);
	visitor.accept(ast)

	return visitor.output.join("\n");
}
class Visitor<VisitorArguments extends any[]> implements VisitorPattern<VisitorArguments> {
	constructor() {

	}

	Program(ast: ProgramNode, ...args: VisitorArguments): void {
			
	}

	public accept(ast: BaseNode, ...args: VisitorArguments) {
		if (!(ast.type in this)) {
			throw new Error(`Visitor not defined for type '${ast.type}'`)
		}

		// @ts-ignore
		return this[ast.type](ast, ...args);
	}
}


class InterferenceGraphVisitor extends Visitor<any> {
	public graph: ScopedInterferenceGraph = new ScopedInterferenceGraph();

	Program(ast: ProgramNode) {
		ast.body.forEach(statement => this.accept(statement))
	}
	FunctionDeclaration(ast: FunctionDeclarationNode) {
		this.graph.enterScope(ast.id.name)
		ast.params.forEach(parameter => this.graph.addVariable(parameter.name))
		ast.body.body.forEach(statement => this.accept(statement))
		this.graph.exitScope()
	}
	IfStatement(ast: IfStatementNode) {
		this.accept(ast.test)
		this.accept(ast.consequent)
		if (ast.alternate) {
			this.accept(ast.alternate)
		}
	}
	BlockStatement(ast: BlockStatementNode) {
		for (const statement of ast.body) {
			this.accept(statement)
		}
	}
	VariableDeclaration(ast: VariableDeclarationNode) {		
		this.accept(ast.declaration)
	}
	VariableDeclarator(ast: VariableDeclaratorNode) {
		this.graph.addVariable(ast.id.name)
		this.accept(ast.init)
	}
	ExpressionStatement(ast: ExpressionStatementNode) {
		this.accept(ast.expression)
	}
	ReturnStatement(ast: ReturnStatementNode) {
		if (ast.argument) {
			this.accept(ast.argument)
		}
	}
	CallExpression(ast: CallExpressionNode) {

	}
	Identifier(ast: IdentifierNode) {
		return ast.name;
	}
	BinaryExpression(ast: BinaryExpressionNode) {
		let left = this.accept(ast.left)
		let right = this.accept(ast.right)

		if (typeof left === "string" && typeof right === "string") {
			this.graph.addEdge(left, right)
		} else if (Array.isArray(left) && typeof right === "string") {
			for (const element of left) {
				if (typeof element === "string") {
					this.graph.addEdge(element, right)
				}
			}
		} else if (typeof left === "string" && Array.isArray(right)) {
			for (const element of right) {
				if (typeof element === "string") {
					this.graph.addEdge(left, element)
				}
			}
		} else if (Array.isArray(left) && Array.isArray(right)) {
			for (const i of left.flat(Infinity)) {
				for (const j of right.flat(Infinity)) {
					if (typeof i === "string" && typeof j === "string") {
						this.graph.addEdge(i, j)
					}
				}
			}
		}

		return left
	}
	Literal(ast: LiteralNode) {
	}
}

class AssemblyGeneratingVisitor extends Visitor<void[]> {
	public output: string[] = [];
	public scope: Scope = new Scope();
	public variableRegisterMap: Record<string, string> = {}
	constructor(public graph: ScopedInterferenceGraph) {
		super();
	}

	FunctionDeclaration(ast: FunctionDeclarationNode) {
		this.scope = this.scope.addFunction(ast.id.name, ast.returntype?.t || "int", ast.params);
		this.graph.enterScope(ast.id.name)
		this.variableRegisterMap = colorScopedGraph(this.graph, ["eax", "ebx", "ecx", "edx"])

		const tempLabel = label.next().value;

		this.output.push(`	jmp ${tempLabel}`,`${ast.id.name}:`, "	push ebp", "	mov ebp, esp");
		
		this.accept(ast.body)

		// Check if the function returns by itself

		if (!ast.body.body.find((node: BaseNode) => node.type === "ReturnStatement")) {
			this.output.push("	pop ebp", "	ret");
		}

		this.output.push(`${tempLabel}:`)

		this.scope = this.scope.parent as Scope;
	}
	Parameter(ast: ParameterNode) {

	}
	Type(ast: TypeNode) {

	}
	ExpressionStatement(ast) {
		this.accept(ast.expression)
	}
	Program(ast: ProgramNode) {
		this.output.push("global _start", "_start:");
		for (const node of ast.body) {
			this.accept(node);
		}

		this.output.push(
			"exit:",
			"	mov ebx, eax",
			"	mov eax, 1",
			"	int 0x80"
		)
	}
	BlockStatement(ast: BlockStatementNode) {
		for (const node of ast.body) {
			this.accept(node);
		}
	}
	CallExpression(ast: CallExpressionNode) {
		const fn = this.scope.lookup(ast.callee.name) as FunctionEntry | null
		if (!fn) {
			throw new Error(`error: function '${ast.callee.name}' doesn't exist in current scope`)
		}

		if (fn.parameters.length !== ast.arguments.length) {
			throw new Error(`error: invalid argument count, expected to see ${fn.parameters.length} but received ${ast.arguments.length}`)
		}

		if (ast.arguments.length > 0) {
			this.output.push(`	sub esp, ${ast.arguments.length * 4}`)
		}

		const parameterList = fn.parameters;

		for (let i = 0; i < ast.arguments.length; i++) {
			const arg = ast.arguments[i];
			const param = parameterList[i]
			const size = getTypeSize(param.type, this.scope);
			if (arg.type === "Literal") {
				this.output.push(`	mov ${getSizeDescriptor(size)} [esp+${i * 4}], ${arg.value}`)
			} else {
				this.accept(arg)
				this.output.push(`	mov ${getSizeDescriptor(size)} [esp+${i * 4}], eax`)
			}
		}
		this.output.push(`	call ${ast.callee.name}`, `	add esp, ${ast.arguments.length * 4}`);
	}
	Decorator(ast: DecoratorNode) {}
	Identifier(ast: IdentifierNode) {
		let symbol = this.scope.lookup(ast.name)

		if (!symbol) {
			throw new Error(`error: '${ast.name}' does not exist in current scope`);
		}

		const register = this.variableRegisterMap[ast.name]
		const size = getTypeSize(ast.t, this.scope)
		this.output.push(`	mov ${register}, ${getSizeDescriptor(size)} [ebp${getOffsetString(symbol.offset)}]`)
		return register
	}
	Literal(ast: LiteralNode) {
		// this.output.push(`	mov ${register}, ${ast.value}`);
	}
	ReturnStatement(ast: ReturnStatementNode) {
		if (ast.argument !== null) {
			const register = this.accept(ast.argument);
			if (register !== "eax") {
				this.output.push(`	mov eax, ${register}`)
			}
		}

		this.output.push("	pop ebp", "	ret");
	}
	VariableDeclaration(ast: VariableDeclarationNode) {		
		for (const decorator of ast.decorators) {
			this.accept(decorator);
		}
		let offset = 0
		if (this.scope.lookup(ast.declaration.id.name)) {
			throw new Error(`error: identifier '${ast.declaration.id.name}' is already declared`)
		} else {
			offset = this.scope.addVariable(ast.declaration.id.name, "ast.declaration.type")
		}

		const register = this.accept(ast.declaration.init);
		this.output.push(
			`	mov [ebp${getOffsetString(offset)}], ${register} # ${ast.declaration.id.name}`
		);
	}
	IfStatement(ast: IfStatementNode) {
		const register = this.accept(ast.test)

		let alternate = label.next().value;
		let consequent = label.next().value

		this.output.push(`	cmp ${register}, eax`, `	jne ${alternate}`)
		this.accept(ast.consequent)
		this.output.push(`	jmp ${consequent}`, `${alternate}:`)
		if (ast.alternate) {
			this.accept(ast.alternate)
		}
		this.output.push(`${consequent}:`)
	}
	BinaryExpression(ast: BinaryExpressionNode) {
		const left = this.accept(ast.left)
		const right = this.accept(ast.right)

		switch(ast.operator) {
			case "<":
				this.output.push(`	leq`)
				break
			case "+":
				this.output.push(`	add ${left}, ${right}`)
				break
			case "==":
				this.output.push(`	cmp ${left}, ${right}`, `	sete al`, `	movzx ${left}, al`)
				break
			case ">":
				this.output.push(`	cmp ${left}, ${right}`, `	setg al`, `	movzx ${left}, al`)
				break
		}

		return left
	}
	VariableDeclarator(ast: VariableDeclaratorNode) {
		
	}
	Struct(ast: StructNode) {
		console.log(JSON.stringify(ast.properties, null, 2));
	}
}

function getOffsetString(offset: number) {
	return `${offset > 0 ? "+" : "-"}${Math.abs(offset)}`
}


const label = labelGenerator()

export function* labelGenerator(prefix: string = "s_"): Generator<string> {
	var i = 0
	while (true) {
		yield `${prefix}${i++}`
	};
}

interface StructNode extends BaseNode {
	type: "Struct";
	properties: any[];
}

const asm = compile(`
fn main(argc: i32, argv: float) {
	var x = 5

	return x
}`);

console.log(asm);