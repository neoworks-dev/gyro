import { test, expect, describe } from "bun:test";
import { parse } from "../src/gyro-parser";

describe("Functions", () => {
	test("Declaring Functions", () => {
		const ast = parse(`fn main() {}`);

		expect(ast).toHaveProperty("type");
		expect(ast.type).toEqual("Program")
		expect(ast).toHaveProperty("body")
		expect(ast.body).toHaveLength(1)
		expect(ast.body[0]).toHaveProperty("type")
		expect(ast.body[0]).toHaveProperty("id")
		expect(ast.body[0]).toHaveProperty("body")
		expect(ast.body[0]).toHaveProperty("params")
		expect(ast.body[0]).toHaveProperty("decorators")
		expect(ast.body[0].type).toEqual("FunctionDeclaration")
		expect(ast.body[0].id).toHaveProperty("type")
		expect(ast.body[0].id).toHaveProperty("name")
		expect(ast.body[0].id.name).toEqual("main")
		expect(ast.body[0].id.type).toEqual("Identifier")
	})
})