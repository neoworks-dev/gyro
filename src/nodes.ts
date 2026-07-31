import { z } from "zod"

export const Identifier = z.object({
	type: z.literal("Identifier"),
	name: z.string()
})

export const Decorator = z.object({

})

export const Literal = z.object({
	type: z.literal("Literal"),
	value: z.union([z.string(), z.number(), z.null(), z.boolean()])
})

export const VariableDeclarator = z.object({
	type: z.literal("VariableDeclarator"),
	init: Literal,
	id: Identifier
})

export const VariableDeclaration = z.object({
	type: z.literal("VariableDeclaration"),
	declarations: z.array(VariableDeclarator)
})

export const FunctionDeclaration = z.object({
	type: z.literal("FunctionDeclaration"),
	id: Identifier,
	decorators: z.array(Decorator),
	params: z.array(z.null()),
	body: z.array(z.union([z.lazy(() => FunctionDeclaration), VariableDeclaration]))
})

export const Program = z.object({
	type: z.literal("Program"),
	body: z.array(z.union([FunctionDeclaration, VariableDeclaration]))
})

