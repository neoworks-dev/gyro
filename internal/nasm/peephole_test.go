package nasm

import "testing"

func TestForwardStores(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			"the load takes the register",
			"\tmov [rbp-8], rax\n\tmov rdx, [rbp-8]\n",
			"\tmov [rbp-8], rax\n\tmov rdx, rax\n",
		},
		{
			"a load into the stored register changes nothing",
			"\tmov [rbp-8], rax\n\tmov rax, qword [rbp-8]\n",
			"\tmov [rbp-8], rax\n\t; (the value is already in rax)\n",
		},
		{
			"a source line marker is not an instruction",
			"\tmov [rbp-8], rbx\n%line 4+0 \"x\"\n\tmov rcx, [rbp-8]\n",
			"\tmov [rbp-8], rbx\n%line 4+0 \"x\"\n\tmov rcx, rbx\n",
		},
		{
			"a label is a way in from somewhere else",
			"\tmov [rbp-8], rax\n.L1:\n\tmov rdx, [rbp-8]\n",
			"\tmov [rbp-8], rax\n.L1:\n\tmov rdx, [rbp-8]\n",
		},
		{
			"another instruction between them is out of reach",
			"\tmov [rbp-8], rax\n\tadd rax, 1\n\tmov rdx, [rbp-8]\n",
			"\tmov [rbp-8], rax\n\tadd rax, 1\n\tmov rdx, [rbp-8]\n",
		},
		{
			"a different slot is left alone",
			"\tmov [rbp-8], rax\n\tmov rdx, [rbp-16]\n",
			"\tmov [rbp-8], rax\n\tmov rdx, [rbp-16]\n",
		},
		{
			"arithmetic and comparison read the register too",
			"\tmov [rbp-8], rax\n\tadd rcx, [rbp-8]\n\tmov [rbp-16], rdx\n\tcmp qword [rbp-16], 0\n",
			"\tmov [rbp-8], rax\n\tadd rcx, rax\n\tmov [rbp-16], rdx\n\tcmp rdx, 0\n",
		},
		{
			"a narrow store is not a word",
			"\tmov byte [rbp-8], al\n\tmov rdx, [rbp-8]\n",
			"\tmov byte [rbp-8], al\n\tmov rdx, [rbp-8]\n",
		},
		{
			"an instruction that writes the slot is not a read of it",
			"\tmov [rbp-8], rax\n\tadd qword [rbp-8], rcx\n",
			"\tmov [rbp-8], rax\n\tadd qword [rbp-8], rcx\n",
		},
	}
	for _, c := range cases {
		if got := forwardStores(c.in); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}
