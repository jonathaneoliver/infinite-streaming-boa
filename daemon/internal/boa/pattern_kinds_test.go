package boa

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"slices"
	"strings"
	"testing"
)

/*
 * EVERY KIND THE PACKAGE DECLARES IS ONE THE VALIDATOR ACCEPTS.
 *
 * validPattern matches kinds with a switch whose default refuses anything
 * unknown. Adding a constant and wiring it into the player is not enough: miss
 * the validator and the new lane is refused at save time with "unknown kind",
 * which reads as the operator having done something wrong. RadioAPDownTell
 * nearly shipped that way, caught by a grep rather than a test (#235).
 *
 * A hand-written list of constants in a test does not close this. Go compiles
 * an unused constant happily, so a list rots exactly as the validator did --
 * and when this test was written, three such lists had: the validator's own
 * "want ..." message, a test in patternnames_test.go, and the example in #235
 * itself, each missing a kind that existed.
 *
 * So the lists are declared once, beside the constants, and checked from both
 * ends: the constants against the lists by reading the source, and the lists
 * against the validator by running it.
 */

// kindLists names each declared list by the prefix its constants carry.
var kindLists = map[string][]string{
	"Link":  linkKinds,
	"Radio": radioKinds,
	"Scope": deadzoneScopes,
}

// A constant declared with a list's prefix and missing from the list is the
// case that nothing else catches.
func TestKindListsNameEveryConstant(t *testing.T) {
	declared := map[string][]string{} // prefix -> values
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, ent := range entries {
		name := ent.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, id := range vs.Names {
					if i >= len(vs.Values) {
						continue
					}
					lit, ok := vs.Values[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					for prefix := range kindLists {
						if strings.HasPrefix(id.Name, prefix) {
							declared[prefix] = append(declared[prefix],
								strings.Trim(lit.Value, "`\""))
						}
					}
				}
			}
		}
	}

	for prefix, list := range kindLists {
		if len(declared[prefix]) == 0 {
			t.Errorf("found no %s* string constants; the source scan is broken, "+
				"so this test would pass whatever the lists hold", prefix)
		}
		for _, v := range declared[prefix] {
			if !slices.Contains(list, v) {
				t.Errorf("%s* constant %q is declared but missing from its list in "+
					"pattern.go, so nothing checks the validator accepts it", prefix, v)
			}
		}
		for _, v := range list {
			if !slices.Contains(declared[prefix], v) {
				t.Errorf("%q is in the %s* list but is not a declared constant", v, prefix)
			}
		}
	}
}

// kindsPattern is the smallest pattern the validator accepts, for an event to
// be added to.
func kindsPattern() Pattern {
	return Pattern{Name: "t", Keys: []Keyframe{{AtSec: 0}, {AtSec: 60}}}
}

// validLink is a well-formed event of each link kind. A kind with no case here
// fails the test that uses it, which is the moment to decide what a valid one
// looks like.
func validLink(t *testing.T, kind string) LinkEvent {
	t.Helper()
	ev := LinkEvent{AtSec: 0, Kind: kind}
	switch kind {
	case LinkDeauth, LinkDisassoc, LinkEvict:
	case LinkDeadzone:
		ev.DurSec = 10
	case LinkPin:
		ev.ToBandMHz = 5180
	default:
		t.Fatalf("no valid example of link kind %q; add one to validLink", kind)
	}
	return ev
}

// validRadio: see validLink.
func validRadio(t *testing.T, kind string) RadioEvent {
	t.Helper()
	ev := RadioEvent{AtSec: 0, Iface: "wlan0", Kind: kind}
	switch kind {
	case RadioGather, RadioEvict, RadioDeauth, RadioScan:
	case RadioOff:
		ev.DurSec = minRadioOffSec
	case RadioAPDown, RadioAPDownTell:
		ev.DurSec = minAPDownSec
	case RadioTxPower:
		dbm := 10.0
		ev.DBm = &dbm
	default:
		t.Fatalf("no valid example of radio kind %q; add one to validRadio", kind)
	}
	return ev
}

func TestTheValidatorAcceptsEveryDeclaredKind(t *testing.T) {
	for _, k := range linkKinds {
		p := kindsPattern()
		p.Links = []LinkEvent{validLink(t, k)}
		if err := validPattern(p); err != nil {
			t.Errorf("declared link kind %q is refused by the validator: %v", k, err)
		}
	}
	for _, k := range radioKinds {
		p := kindsPattern()
		p.Radios = []RadioEvent{validRadio(t, k)}
		if err := validPattern(p); err != nil {
			t.Errorf("declared radio kind %q is refused by the validator: %v", k, err)
		}
	}
	for _, s := range append([]string{""}, deadzoneScopes...) {
		p := kindsPattern()
		ev := validLink(t, LinkDeadzone)
		ev.Scope = s
		p.Links = []LinkEvent{ev}
		if err := validPattern(p); err != nil {
			t.Errorf("declared deadzone scope %q is refused by the validator: %v", s, err)
		}
	}
}

// The mirror, so the test above cannot be satisfied by a validator that
// accepts everything -- and the refusal names every kind it would have taken.
func TestTheValidatorRefusesAnUndeclaredKind(t *testing.T) {
	const bogus = "not-a-kind"

	p := kindsPattern()
	p.Links = []LinkEvent{{AtSec: 0, Kind: bogus}}
	checkRefusal(t, "link kind", validPattern(p), linkKinds)

	p = kindsPattern()
	p.Radios = []RadioEvent{{AtSec: 0, Iface: "wlan0", Kind: bogus}}
	checkRefusal(t, "radio kind", validPattern(p), radioKinds)

	p = kindsPattern()
	ev := validLink(t, LinkDeadzone)
	ev.Scope = bogus
	p.Links = []LinkEvent{ev}
	checkRefusal(t, "deadzone scope", validPattern(p), deadzoneScopes)
}

func checkRefusal(t *testing.T, what string, err error, want []string) {
	t.Helper()
	if err == nil {
		t.Errorf("an undeclared %s was accepted", what)
		return
	}
	for _, k := range want {
		if !strings.Contains(err.Error(), k) {
			t.Errorf("refusing an undeclared %s, the message does not offer %q: %v",
				what, k, err)
		}
	}
}
