package cc

import "testing"

func TestSwitchDispatches(t *testing.T) {
	source := `
static int classify(int n) {
    switch (n) {
    case 0:
        return 100;
    case 1:
    case 2:
        return 200;
    case 7:
        return 700;
    default:
        return -1;
    }
}
int result(void) {
    return classify(0) + classify(1) + classify(2) + classify(7) + classify(9);
}
`
	answers(t, source, 100+200+200+700-1)
}

func TestSwitchFallsThrough(t *testing.T) {
	answers(t, `
int result(void) {
    int total;
    int i;
    total = 0;
    for (i = 0; i < 4; i++) {
        switch (i) {
        case 0:
            total = total + 1;
        case 1:
            total = total + 10;
            break;
        case 2:
            total = total + 100;
        default:
            total = total + 1000;
        }
    }
    return total;
}
`, (1+10)+(10)+(100+1000)+(1000))
}

func TestSwitchDefaultInTheMiddle(t *testing.T) {
	answers(t, `
static int f(int n) {
    int out;
    out = 0;
    switch (n) {
    case 1:
        out = 1;
        break;
    default:
        out = 9;
        break;
    case 2:
        out = 2;
        break;
    }
    return out;
}
int result(void) { return f(1) * 100 + f(2) * 10 + f(5); }
`, 100+20+9)
}

func TestSwitchInsideALoop(t *testing.T) {
	// `break` belongs to the switch and `continue` to the loop.
	answers(t, `
int result(void) {
    int i, total;
    total = 0;
    for (i = 0; i < 6; i++) {
        switch (i) {
        case 2:
            continue;
        case 3:
            break;
        default:
            total = total + i;
            break;
        }
        total = total + 100;
    }
    return total;
}
`, (0+100)+(1+100)+(0)+(100)+(4+100)+(5+100))
}

func TestSwitchOnEnumsAndChars(t *testing.T) {
	answers(t, `
enum Color { RED, GREEN, BLUE };
static int weight(enum Color c) {
    switch (c) {
    case RED: return 1;
    case GREEN: return 2;
    case BLUE: return 4;
    }
    return 0;
}
int result(void) { return weight(RED) + weight(GREEN) * 10 + weight(BLUE) * 100; }
`, 1+20+400)

	answers(t, `
static int digit(char c) {
    switch (c) {
    case '0': case '1': case '2': return 1;
    case 'a': return 2;
    }
    return 0;
}
int result(void) { return digit('1') + digit('a') * 10 + digit('z') * 100; }
`, 1+20)
}

func TestRefusesBadSwitches(t *testing.T) {
	refused(t, `int result(void) { switch (1) { case 1: case 1: return 0; } return 0; }`,
		"case 1 appears twice")
	refused(t, `int result(void) { switch (1) { default: default: return 0; } return 0; }`,
		"two defaults")
	refused(t, `int result(void) { switch (1) { int x; case 1: return 0; } return 0; }`,
		"unreachable")
	refused(t, `int result(void) { case 1: return 0; }`, "not inside a switch")
}
