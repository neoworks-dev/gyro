.PHONY: all test

all: compile-parser run-compiler

run-compiler:
	bun run ./src/compiler.ts

compile-parser:
	bunx peggy -o src/gyro-parser-v1.js src/gyro.peggy

test:
	bunx peggy src/gyro.peggy --test-file src/test.gyro

compile:
	nasm -F dwarf -g -f elf32 -o $(in).o $(in) 
	ld -m elf_i386 -o $(out) $(in).o
	rm $(in).o