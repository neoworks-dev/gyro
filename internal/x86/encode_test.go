package x86

import (
	"bytes"
	"debug/elf"
	"encoding/hex"
	"strings"
	"testing"
)

// encodeLine assembles one line of the backend's text on its own.
func encodeLine(t *testing.T, line string) (encoded, error) {
	t.Helper()
	stmts, err := parseStatement(line)
	if err != nil {
		return encoded{}, err
	}
	if len(stmts) != 1 || stmts[0].kind != stmtInst {
		t.Fatalf("%q is not one instruction", line)
	}
	return encodeInst(stmts[0].prefix, stmts[0].mnemonic, stmts[0].operands)
}

// The expected bytes are worked out from the encoding rules in Intel's
// manual, not copied from an assembler, and each case is one of the
// places those rules have an exception.
func TestEncodings(t *testing.T) {
	cases := []struct{ line, want, why string }{
		{"mov rax, rbx", "4889d8", "register to register is the 89 form"},
		{"mov rbp, rsp", "4889e5", ""},
		{"mov r12, [rsp]", "4c8b2424", "rsp as a base needs a SIB byte"},
		{"mov rax, [r12+8]", "498b442408", "r12 as a base needs a SIB byte too"},
		{"mov rax, [rbp]", "488b4500", "rbp as a base has no form without a displacement"},
		{"mov rax, [r13]", "498b4500", "nor does r13"},
		{"mov rax, [rbp-200]", "488b8538ffffff", "a displacement past a byte takes 32 bits"},
		{"mov [rbp-8], rax", "488945f8", ""},
		{"lea rax, [rdx+r12*4]", "4a8d04a2", "r12 may be an index; REX.X carries its fourth bit"},
		{"lea rax, [rbp+rax]", "488d440500", "the first register is the base"},
		{"lea rax, [rax+rbp]", "488d0428", ""},
		{"movzx eax, sil", "400fb6c6", "sil exists only under a REX prefix"},
		{"mov sil, al", "4088c6", ""},
		{"mov r8b, al", "4188c0", ""},
		{"setae r13b", "410f93c5", ""},
		{"movzx r13d, r13b", "450fb6ed", ""},
		{"movzx rax, byte [rdx+rbx]", "480fb6041a", ""},
		{"movsx rax, word [rdx]", "480fbf02", ""},
		{"movsxd rcx, dword [rdx]", "48630a", ""},
		{"movsxd rax, eax", "4898", "nasm writes it as cdqe"},
		{"movsx eax, ax", "98", "and this as cwde"},
		{"add rax, 1000", "4805e8030000", "the accumulator has its own immediate form"},
		{"add rcx, 1000", "4881c1e8030000", ""},
		{"add rax, 1", "4883c001", "an immediate that fits a signed byte takes one"},
		{"sub rsp, 127", "4883ec7f", ""},
		{"sub rsp, 128", "4881ec80000000", "128 does not fit a signed byte"},
		{"cmp rax, -129", "483d7fffffff", ""},
		{"and eax, 4294967295", "83e0ff", "as a dword, 0xffffffff is -1"},
		{"cmp al, 5", "3c05", ""},
		{"and r8b, cl", "4120c8", ""},
		{"xor eax, eax", "31c0", ""},
		{"cmp rax, [rbp-16]", "483b45f0", ""},
		{"cmp qword [rbp-16], 0", "48837df000", ""},
		{"test rax, rax", "4885c0", ""},
		{"test rax, 8", "48a908000000", "test has no byte-immediate form"},
		{"mov rax, 4", "b804000000", "a small 64-bit constant is a zero-extending 32-bit move"},
		{"mov rax, 4294967295", "b8ffffffff", ""},
		{"mov rax, -1", "48c7c0ffffffff", "a negative one sign-extends"},
		{"mov rax, 18446744073709551615", "48c7c0ffffffff", "which is the same constant"},
		{"mov r9, 4294967296", "49b90000000001000000", "only past both does it take ten bytes"},
		{"mov r12, 0", "41bc00000000", ""},
		{"mov qword [rbp-8], -5", "48c745f8fbffffff", ""},
		{"mov word [rax], 5", "66c7000500", ""},
		{"mov byte [rax], 5", "c60005", ""},
		{"mov dword [rbp-72], eax", "8945b8", ""},
		{"imul rax, r12, 4", "496bc404", ""},
		{"imul rax, rax, 1000000000", "4869c000ca9a3b", ""},
		{"imul rax, rcx", "480fafc1", ""},
		{"mul qword [rbp-8]", "48f765f8", ""},
		{"idiv rcx", "48f7f9", ""},
		{"not rax", "48f7d0", ""},
		{"shl rax, 1", "48d1e0", "a shift by one has its own opcode"},
		{"shl rax, cl", "48d3e0", ""},
		{"sar r10, 3", "49c1fa03", ""},
		{"btr rax, 63", "480fbaf03f", ""},
		{"push r12", "4154", ""},
		{"pop rbx", "5b", ""},
		{"call r11", "41ffd3", ""},
		{"call rax", "ffd0", "an indirect call needs no REX.W"},
		{"rep movsb", "f3a4", ""},
		{"rep stosb", "f3aa", ""},
		{"cqo", "4899", ""},
		{"syscall", "0f05", ""},
		{"movsd xmm0, [rbp-8]", "f20f1045f8", ""},
		{"movsd [rbp-8], xmm8", "f2440f1145f8", "REX comes after the mandatory prefix"},
		{"movsd xmm0, xmm1", "f20f10c1", ""},
		{"movss xmm0, dword [rdx]", "f30f1002", ""},
		{"movss dword [rdx], xmm0", "f30f1102", ""},
		{"movq xmm0, rax", "66480f6ec0", ""},
		{"movq rax, xmm0", "66480f7ec0", ""},
		{"movd eax, xmm0", "660f7ec0", ""},
		{"cvtsi2sd xmm1, r9", "f2490f2ac9", ""},
		{"cvttsd2si rax, xmm0", "f2480f2cc0", ""},
		{"cvtsd2ss xmm0, xmm0", "f20f5ac0", ""},
		{"ucomisd xmm0, [rbp-16]", "660f2e45f0", ""},
		{"ucomiss xmm0, xmm1", "0f2ec1", ""},
		{"xorpd xmm0, xmm0", "660f57c0", ""},
		{"sqrtsd xmm0, xmm0", "f20f51c0", ""},
		{"roundsd xmm0, xmm1, 9", "660f3a0bc109", ""},
	}
	for _, c := range cases {
		got, err := encodeLine(t, c.line)
		if err != nil {
			t.Errorf("%s: %v", c.line, err)
			continue
		}
		if hex.EncodeToString(got.bytes) != c.want {
			t.Errorf("%s: got % x, want %s (%s)", c.line, got.bytes, c.want, c.why)
		}
		if got.fix != nil {
			t.Errorf("%s: unexpected fixup", c.line)
		}
	}
}

// A symbol in brackets is rip-relative. The field holds the target less
// the address after the instruction, so an immediate behind the field
// moves the addend.
func TestRipRelative(t *testing.T) {
	cases := []struct {
		line   string
		want   string
		offset int
		addend int64
	}{
		{"lea rax, [S_0]", "488d0500000000", 3, -4},
		{"mov rax, [M_m+16]", "488b0500000000", 3, 12},
		{"mov qword [G_b+8], 5", "48c7050000000005000000", 3, 0},
		{"cmp byte [G_b], 1", "803d0000000001", 2, -5},
		{"movsd xmm9, [G_c]", "f2440f100d00000000", 5, -4},
	}
	for _, c := range cases {
		got, err := encodeLine(t, c.line)
		if err != nil {
			t.Errorf("%s: %v", c.line, err)
			continue
		}
		if hex.EncodeToString(got.bytes) != c.want {
			t.Errorf("%s: got % x, want %s", c.line, got.bytes, c.want)
		}
		if got.fix == nil || !got.fix.pcrel || got.fix.offset != c.offset || got.fix.addend != c.addend {
			t.Errorf("%s: fixup %+v, want pc-relative at %d with addend %d", c.line, got.fix, c.offset, c.addend)
		}
	}
}

// What the processor cannot encode is an error, never a guess.
func TestEncodingErrors(t *testing.T) {
	for _, line := range []string{
		"mov qword [rax], 2147483648", // no sign-extended dword holds it
		"add rax, 4294967295",
		"mov byte [rax], 256",
		"mov [rax], 5",         // no operand size
		"mov rax, [rax+rsp*2]", // rsp cannot be an index
		"mov eax, rbx",         // sizes differ
		"mov rax, [S_0+rbx]",   // a symbol address takes no registers
		"lea rax, rbx",
		"movzx rax, rbx",
		"frobnicate rax",
	} {
		if _, err := encodeLine(t, line); err == nil {
			t.Errorf("%s: no error", line)
		}
	}
}

// A jump to a label is short when the target is within a signed byte of
// the jump's end, both ways.
func TestJumpSizes(t *testing.T) {
	filler := func(n int) string {
		return strings.Repeat("\tnop\n", n)
	}
	cases := []struct {
		name, source, want string
	}{
		{"forward 127", "jmp .a\n" + filler(127) + ".a:\n", "eb7f"},
		{"forward 128", "jmp .a\n" + filler(128) + ".a:\n", "e980000000"},
		{"backward 128", ".a:\n" + filler(126) + "jz .a\n", "7480"},
		{"backward 129", ".a:\n" + filler(127) + "jz .a\n", "0f847bffffff"},
	}
	for _, c := range cases {
		object, err := Assemble("section .text\nf:\n"+c.source, "test.asm")
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		file, err := elf.NewFile(bytes.NewReader(object))
		if err != nil {
			t.Fatal(err)
		}
		text, _ := file.Section(".text").Data()
		// The filler is nop, 0x90, on both sides of the jump.
		start, end := 0, len(text)
		for start < end && text[start] == 0x90 {
			start++
		}
		for end > start && text[end-1] == 0x90 {
			end--
		}
		jump := hex.EncodeToString(text[start:end])
		if jump != c.want {
			t.Errorf("%s: got %s, want %s", c.name, jump, c.want)
		}
	}
}
