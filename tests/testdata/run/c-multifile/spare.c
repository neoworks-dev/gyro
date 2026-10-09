/* Nothing in this program needs anything here, so this file is never
 * lowered. A directory is not a build target: the entry file's needs
 * are, and a resolver that compiled everything beside it would make an
 * unrelated file in a folder into a dependency. */

int spare_thing(void) { return 999; }
