export interface BaseNode {
	type:
		| "Program"
		| "FunctionDeclaration"
		| "BlockStatement"
		| "ReturnStatement"
		| "VariableDeclaration"
		| "VariableDeclarator"
		| "CallExpression"
		| "Identifier"
		| "Decorator"
		| "Literal"
		| "Struct"
		| "IfStatement"
		| "BinaryExpression"
		| "Type"
		| "Parameter";
}

export interface VisitorPattern<VisitorArguments extends any[]> {
	Program(ast: ProgramNode, ...args: VisitorArguments): void;
}

export interface BinaryExpressionNode extends BaseNode {
	operator: string,
	left: BaseNode,
	right: BaseNode
}

export interface IfStatementNode extends BaseNode {
	test: BaseNode,
	consequent: BlockStatementNode,
	alternate: BlockStatementNode
}

export interface ProgramNode extends BaseNode {
	type: "Program";
	body: BaseNode[];
}

export interface FunctionDeclarationNode extends BaseNode {
	type: "FunctionDeclaration";
	id: IdentifierNode;
	decorators: DecoratorNode[];
	returntype: TypeNode,
	params: ParameterNode[];
	body: BlockStatementNode;
}

export interface ParameterNode extends BaseNode {
	type: "Parameter"
	name: string,
	t: TypeNode
}

export interface BlockStatementNode extends BaseNode {
	type: "BlockStatement";
	body: BaseNode[];
}

export interface IdentifierNode extends BaseNode {
	type: "Identifier";
	name: string;
}

export interface DecoratorNode extends BaseNode {
	type: "Decorator";
	name: string;
	// TODO
	params: any;
}

export interface ReturnStatementNode extends BaseNode {
	type: "ReturnStatement";
	argument: BaseNode | null;
}

export interface VariableDeclarationNode extends BaseNode {
	type: "VariableDeclaration";
	name: IdentifierNode;
	init: LiteralNode | IdentifierNode;
	t: TypeNode;
	kind: "var" | "const" | "let";
	decorators: DecoratorNode[];
}

export interface VariableDeclaratorNode extends BaseNode {
	type: "VariableDeclarator";
	// TODO
	init: LiteralNode;
	id: IdentifierNode;
}

export interface LiteralNode extends BaseNode {
	type: "Literal";
	value: boolean | string | number;
}

export interface CallExpressionNode extends BaseNode {
	type: "CallExpression";
	// TODO
	callee: IdentifierNode;
	arguments: (LiteralNode | IdentifierNode)[];
}

export interface TypeNode extends BaseNode {
	type: "Type"
	t: string
}