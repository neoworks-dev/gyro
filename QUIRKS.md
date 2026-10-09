# Quirks

What writing an ordinary program in Gyro ran into. Found building
`tests/testdata/run/rpn/` (an RPN calculator: 970 lines, seven modules, a
REPL) and `tests/testdata/run/m6502/` (a 6502 core against the
SingleStepTests suite), 2026-08-29.

An item is a surprise, a gap or a wrong-looking diagnostic — not a bug
report and not a design proposal. Where the answer is already decided,
the entry says so. The issues on `neoworks-dev/gyro` hold the open work,
and `docs/decisions.md` what is
settled.

## Surprises in the language

- [ ] **A method on a user type cannot cross a module.** `Calc::binary`
      in `builtins.gyro` gives `"./builtins" does not export binary` at
      the import and `Calc has no method run` at the call, though the
      declaration says `export`. Only built-in types can be extended from
      elsewhere ([chapter 5.2](docs/ch05-02-methods.md)), so a user type's
      whole surface belongs to the module that declares it. This is what
      decided the calculator's file layout: `Calc` keeps its methods in
      `machine.gyro`, and every other module is `fn word(calc: *Calc,
      ...)`. The rule may well be right; the diagnostic is not. It says a
      name is not exported when it is exported and merely not importable
      *as a method*, and the chapter says built-ins **can** be extended
      without ever saying user types cannot be.
- [ ] **A `match` arm cannot be an assignment.** `Mode::Zp: self.ea = x`
      is `E0205 expected a pattern, found =`, because an arm body is an
      expression, a block or a control-flow statement and an assignment is
      none of the three. The fix is `{ }` around it, which is fine once
      known — the error names the `=` rather than the arm, so it reads
      like a syntax error in the pattern.
- [ ] **A `const` in a match pattern is silently a binding.** Patterns are
      literals, bindings, variant destructuring and type tests, so
      `match mode { IMP: ... }` matches everything and binds `IMP` instead
      of comparing against the constant. It compiles and runs wrong. This
      pushed the 6502 dispatch to enums, which was the better program —
      but nothing said so at the time.
- [ ] **A fixed-array length must be a literal.** `touched:
      u16[TOUCHED_LIMIT]` is `E0501 cannot use u16[64] as u16[?]`, so a
      size lives twice: as a `const` for the bounds check and as a literal
      in the type. Not mentioned in
      [chapter 3.2](docs/ch03-02-data-types.md), which only says `T[N]`
      sizes are `usize`.
- [ ] **Every index loop is a `while`.** `for i in 0..151` gives an `i32`
      and indexing wants `usize`, so the range form is unusable for
      indexing and each loop becomes `var i: usize = 0` plus `while`. A
      range does not take its element type from what the variable is used
      for.
- [ ] **`message()` returning a field needs `!{ &self }`.** Borrow
      provenance reaches the error contract: `fn CalcError::message(self):
      string` returning `self.text` is rejected until the row names the
      receiver. Correct, and the error type is the last place one expects
      to think about views.

## Gaps a program hits immediately

- [x] **No line reader.** Fixed 2026-08-29: `os::readline(): string?`,
      null at end of file — runtime, backend mapping, docs, decision entry
      and `tests/testdata/run/os-readline.gyro`. Nothing interactive could
      be written before it, because `read_stdin` only returns at EOF.
- [ ] **No float formatting.** `println` is Go's shortest round-trip, so a
      calculator prints `10!` as `3.6288e+06`, and `fmt` has no float verb
      at all — no `%f`, no precision — so a program cannot ask for fixed
      notation. This is the worst of the gaps found: it is not a missing
      convenience, it is output a person cannot read.
- [ ] **`math` has no `pow`, `exp`, `log` or trig.** Every name in it is a
      machine instruction on purpose, and those are not. `^` in the
      calculator therefore takes whole exponents and multiplies
      repeatedly. **The open question is whether `math` may call libm at
      all** — one tier makes it safe today, and the "no library to agree
      with" argument is why it does not.
- [ ] **`split` takes a literal separator only.** No whitespace split, so
      the calculator tokenizes on `" "` and a tab breaks a line.
- [ ] **Deterministic listings need a second structure.** `Map` iterates
      in hash order and `keys()` allocates the whole key array, so both
      `words` and `vars` keep an insertion-ordered `Array<string>` beside
      the map to print in the order a person typed. Sorting would not fix
      it either — there is none ([appendix G](docs/appendix-g-known-gaps.md)).
- [ ] **`Array<T>` has no `remove`.** `forget` rebuilds the name array
      without the name.
- [ ] **`String::text()` copies every call**, so building a line of output
      allocates once per read-back rather than once.

## In the harness

- [ ] **`//gyro:stdin` trims its argument**, so a corpus case cannot feed
      a line with leading spaces. Found writing the `readline` case, whose
      first golden claimed indentation the directive had already removed.

## What held up

Not a checklist — the parts that made the program shorter than the C it
would have been.

- **`(T, error?)` with a duck-typed `error`.** The calculator's error type
  is thirteen lines including its comment, `if err != null` reads like
  prose, and dropping an error is a compile error rather than a habit.
- **Nullable narrowing.** `if line == null { break }` and afterwards
  `line` is a `string`. No unwrap operator, no shadowing dance, and it
  follows early exits out of loops.
- **Multi-value returns fit a parser exactly.** `(usize, error?)` is "how
  far did you get, and did it fail", which threaded the token index
  through definitions and branches with no cursor struct.
- **Slice narrowing made control flow cheap.** `run(calc, words[at..else],
  depth)` *is* the then-branch: no copy, no index arithmetic in the
  recursion, and `if ... else ... then` came to about 25 lines.
- **`defer` on the line after the allocation**, every time.
- **Enums plus exhaustive `match`** carried the 6502's 64-arm dispatch,
  where C used two tables of function pointers and pointer identity.
- **The golden discipline earned its keep twice**: it caught a hand-written
  expectation of `3628800` that Go's formatting spells `3.6288e+06`, and
  it caught 38 real bugs in the emulator being ported.
- **Seven modules build to a native binary in about a third of a second.**
