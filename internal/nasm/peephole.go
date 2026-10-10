package nasm

import (
	"regexp"
	"strings"
)

// The emitters keep every METALS register in a frame slot and write each
// instruction's result there, so the next instruction that reads it loads
// it straight back. The store has to happen — the slot is the register's
// home — but the load waits on the store to forward its value, which is
// most of what a short chain of field and pointer reads costs.
//
// forwardStores removes that wait. When an instruction stores a register
// to a frame slot and the very next one loads the same slot, the load
// takes the register instead. Nothing between the two can change either
// side, and nothing else is touched: the store stays, so a later reader
// of the slot still finds its value.

var (
	storeToSlot  = regexp.MustCompile(`^\tmov (?:qword )?\[(rbp-\d+)\], (r[a-z0-9]+)$`)
	loadFromSlot = regexp.MustCompile(`^\tmov (r[a-z0-9]+), (?:qword )?\[(rbp-\d+)\]$`)
	// An instruction that reads a slot as its last operand and leaves the
	// slot as it was, or compares it against something.
	readsSlot     = regexp.MustCompile(`^\t(add|sub|and|or|xor|cmp|imul|test) (r[a-z0-9]+), (?:qword )?\[(rbp-\d+)\]$`)
	compareSlot   = regexp.MustCompile(`^\t(cmp|test) qword \[(rbp-\d+)\], (-?\d+|r[a-z0-9]+)$`)
	wordRegisters = map[string]bool{
		"rax": true, "rbx": true, "rcx": true, "rdx": true, "rsi": true, "rdi": true,
		"r8": true, "r9": true, "r10": true, "r11": true,
		"r12": true, "r13": true, "r14": true, "r15": true,
	}
)

func forwardStores(text string) string {
	lines := strings.Split(text, "\n")
	slot, held := "", ""
	for index, line := range lines {
		// A source-line marker emits nothing, so it does not separate two
		// instructions.
		if strings.HasPrefix(line, "%line ") {
			continue
		}
		if held != "" {
			if match := loadFromSlot.FindStringSubmatch(line); match != nil && match[2] == slot {
				if match[1] == held {
					lines[index] = "\t; (the value is already in " + held + ")"
				} else {
					lines[index] = "\tmov " + match[1] + ", " + held
				}
			} else if match := readsSlot.FindStringSubmatch(line); match != nil && match[3] == slot {
				lines[index] = "\t" + match[1] + " " + match[2] + ", " + held
			} else if match := compareSlot.FindStringSubmatch(line); match != nil && match[2] == slot {
				lines[index] = "\t" + match[1] + " " + held + ", " + match[3]
			}
		}
		slot, held = "", ""
		if match := storeToSlot.FindStringSubmatch(line); match != nil && wordRegisters[match[2]] {
			slot, held = match[1], match[2]
		}
	}
	return strings.Join(lines, "\n")
}
