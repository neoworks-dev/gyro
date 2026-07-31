## Git

You're allowed to use git. Every time you make a big change, commit the current worktree if it's dirty before changing anything, then write your changes, choose a short to-the-point commit message, and push to the remote repository. If you are unsure about what to write in the commit message, ask for help. Write an explanation of the change in the body of the commit message if it is not obvious from the title. Always mention that a commit was made by you and not an actual human.

## Code Style

When editing or generating code, prioritize readability and maintainability over cleverness.

Rules:

- Use Svelte specific coding. Svelte has features for dynamic class assignments like `class:rotate-0={open}`, prefer those over `class={open ? "rotate-0" : "rotate-90"}`

- Preserve descriptive names. Do not shorten identifiers.
  - Good: `recordId`, `customerAccount`, `paymentMethod`
  - Bad: `rid`, `acct`, `pm`

- Use `camelCase` for variables, functions, parameters, and object fields unless the language, framework, or existing codebase requires another convention.
- Prefer explicit control flow over shorthand.
- Avoid `??`, ternary `?:`, and compact conditional expressions unless they clearly prevent a large amount of repetitive code without reducing readability.
- Do not deeply nest logic.
  - More than 2 indentation levels is too much.
  - Use guard clauses, early returns, helper functions, or extracted validation steps instead.

- Keep functions short and focused.
  - Split large functions into smaller functions instead of writing one huge function.
  - Each function should have one clear responsibility.

- Add comments only when the code is not immediately readable when skimming.
- Comments must be technical, concise, and useful.
  - Good: `// Normalize external IDs before database lookup.`
  - Bad: `// Now we loop through the items and do the thing.`

- Do not add obvious comments that restate the code.
- Prefer clearly named helper functions over long inline logic with comments.
- Do not change behavior, public APIs, data shapes, validation rules, or side effects unless explicitly asked.
- Match the surrounding code style when it conflicts with these rules.

## Tests

After having written all your changes, think about if any of these changes require writing a test, if so, create a test in the tests/ directory and run the `bun test` command to see if any of the tests are failing, if they do, investigate further, otherwise everything is fine and you're done.
There are two distinct testing frameworks used in this project, `bun test` is used for simple integration tests whereas playwright is used for more advanced end-to-end tests that require browser automation, if you are adding a test that requires browser automation, write a playwright test and run `bunx playwright test` to see if any of the tests are failing.
