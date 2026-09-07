package boa

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// The wire contract is hand-mirrored: model.go's doc comment names itself the
// single source of truth, and ui/src/types.ts says it mirrors model.go. Nothing
// enforced that. Adding a field to a response struct and forgetting the UI is a
// SILENT failure -- the JSON carries the field, TypeScript never names it, no
// build breaks and no request errors. The value simply never reaches the
// screen, which is indistinguishable from a collector that stopped working.
//
// This test is the loud version. It walks every type reachable from a response
// the UI consumes and asserts the UI has a name for each field.
//
// It deliberately does NOT check types, only presence. Presence is what silence
// hides; a wrong TypeScript type surfaces the moment the value is rendered.

// uiSrcDir is the interface source, relative to this package.
//
// The whole tree, not just types.ts. types.ts is the type mirror, but plenty of
// fields are named only where they are used -- bucket_ms and phy_down are read
// in composables/useSnapshot.ts and appear in no interface. Checking types.ts
// alone reports those as unreachable when they are plumbed and working.
const uiSrcDir = "../../../ui/src"

// wireRoot ties a response payload to the endpoint that serves it.
type wireRoot struct {
	// Endpoint is the route as api.go registers it, or a short phrase for a
	// payload that is nested rather than served whole.
	Endpoint string
	// Value is a zero value of the type written to the response body.
	Value any
}

// wireRoots is every payload shape the UI reads, and the single source both the
// drift test and the generated API reference work from. A new GET endpoint
// whose payload the UI consumes belongs here.
//
// Endpoints serialising an anonymous map[string]any (/api/health, /api/history's
// envelope) have no type to reflect over and are covered by handWrittenKeys.
//
// Config is deliberately absent: it is the daemon's runtime configuration and
// never reaches the wire. GET /api/config serves ConfigExport.
var wireRoots = []wireRoot{
	{"GET /api/state", Snapshot{}},
	{"GET /api/bridge", BridgeInfo{}},
	{"GET /api/events", Event{}},
	{"GET /api/history (clients[mac][])", Sample{}},
	{"GET /api/config", ConfigExport{}},
	{"GET /api/bridge/radios/{iface}/survey", SurveyResult{}},
}

// handWrittenKeys are response keys built by map literal rather than a struct,
// so no reflect walk can find them. Listed here so they are still checked.
var handWrittenKeys = []string{
	"ok", "caps", "revision", // GET /api/health
	"interval_ms", "bucket_ms", "window_sec", "now", "clients", // GET /api/history
}

// serverOnly are wire fields the UI is not expected to name, each with the
// reason it is exempt. An entry here is a claim that the field is invisible to
// the interface BY DESIGN -- not a place to park a field the UI should have.
// A field that the interface OUGHT to show belongs in knownGaps instead.
var serverOnly = map[string]string{}

// knownGaps is what this test found the day it was written: fields the daemon
// already sends that no interface source names. It is a baseline to burn down,
// not a list of exemptions -- every entry is a value being computed, serialised
// and thrown away.
//
// The test fails on anything NOT in this list, so the set cannot grow. It also
// fails on an entry that is no longer missing, so the list cannot go stale:
// plumb a field through and this test tells you to delete its line.
//
// Nothing here is asserted to be unimportant. Several look like the diagnostic
// they were added to be, never wired to a screen -- see the Go doc comment on
// each field for what it is for.
var knownGaps = map[string]string{
	"looked":         "BridgeInfo.Scans.Looked -- records that a channel was listened to and heard nothing",
	"socket":         "Snapshot.Caps.Adapter.Socket -- physical USB port, the tiebreak when MAC and iface disagree",
	"names_learned":  "Snapshot.Caps.NamesLearned -- mDNS bindings seen; 0 distinguishes a dead capture socket from a quiet client",
	"names_by_mac":   "Snapshot.Caps.NamesByMAC -- the MAC-keyed count, which is the one that proves the filtered socket opened",
	"throttle":       "Snapshot.Clients.Policy.Ladders.Throttle -- the starved-client measurement point",
	"delivered_mbps": "…Throttle.DeliveredMbps -- rate delivered under a known cap",
	"ratio":          "…Throttle.Ratio",
	"variation":      "…Throttle.Variation",
	"recipe":         "Snapshot.Clients.Policy.Pattern.Recipe -- how a merged pattern was built",
	"dwell_sec":      "…Recipe.DwellSec",
	"sources":        "…Recipe.Sources",
	"duration_known": "Snapshot.Clients.Station.DurationKnown -- whether the driver answers at all; without it an idle radio and a silent driver both read 0%",
	"tx_duration_us": "Snapshot.Clients.Station.TxDurationUs -- airtime, verified against iperf3 to 0.7% (DATA-CONTRACT Source T)",
	"rx_duration_us": "Snapshot.Clients.Station.RxDurationUs -- as above",
	"suspect_skip":   "Snapshot.Clients.Sweep.Levels.SuspectSkip -- a jump wide enough to hide a rendition",
	"pass":           "Snapshot.Clients.Sweep.Pass -- \"map\" vs \"measure\"; the two passes use opposite window lengths",
	"interval_ms":    "GET /api/history -- the live tick. api.go says it exists so the chart cannot claim a resolution it does not have; useSnapshot.ts reads only bucket_ms, so that safeguard is not wired up",
	"window_sec":     "GET /api/history -- the window the series covers",
}

func TestWireContractMirroredInUI(t *testing.T) {
	ui, err := uiFieldNames()
	if err != nil {
		// A missing or unreadable mirror fails the test rather than skipping
		// it. A skip here would be the same silence the test exists to break.
		t.Fatalf("reading the UI mirror: %v", err)
	}

	type miss struct{ field, path string }
	var missing []miss
	checked := map[string]bool{}
	stillMissing := map[string]bool{}

	record := func(field, path string) {
		if checked[field] {
			return
		}
		checked[field] = true
		if ui[field] || serverOnly[field] != "" {
			return
		}
		if _, baselined := knownGaps[field]; baselined {
			stillMissing[field] = true
			return
		}
		missing = append(missing, miss{field, path})
	}

	for _, root := range wireRoots {
		rt := reflect.TypeOf(root.Value)
		walkWireFields(rt, rt.Name(), map[reflect.Type]bool{}, record)
	}
	for _, k := range handWrittenKeys {
		record(k, "api.go (map literal)")
	}

	if len(missing) > 0 {
		sort.Slice(missing, func(i, j int) bool { return missing[i].path < missing[j].path })
		var b strings.Builder
		b.WriteString("new wire fields that no source under " + uiSrcDir + " names:\n\n")
		for _, m := range missing {
			b.WriteString("  " + m.field + "\tat " + m.path + "\n")
		}
		b.WriteString("\nEach is serialised into a response and cannot reach the screen.\n")
		b.WriteString("Name it in the interface, or -- if it is server-side by design --\n")
		b.WriteString("add it to serverOnly with the reason why.\n")
		t.Error(b.String())
	}

	// A baselined field that the interface now names is a gap that got closed.
	// Saying so is the point: the list has to shrink, and a stale entry would
	// quietly re-admit the field it names.
	var closed []string
	for field := range knownGaps {
		if !stillMissing[field] {
			closed = append(closed, field)
		}
	}
	if len(closed) > 0 {
		sort.Strings(closed)
		t.Errorf("knownGaps entries that are no longer gaps: %s\n\n"+
			"The interface now names these (or the field left the wire).\n"+
			"Delete their lines from knownGaps -- while listed, real drift in\n"+
			"them would be ignored.", strings.Join(closed, ", "))
	}
}

// walkWireFields calls fn for every JSON field name reachable from t, passing
// the dotted Go path that reaches it so a failure says where to look.
func walkWireFields(t reflect.Type, path string, seen map[reflect.Type]bool, fn func(field, path string)) {
	for t.Kind() == reflect.Ptr || t.Kind() == reflect.Slice || t.Kind() == reflect.Array || t.Kind() == reflect.Map {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return
	}
	// time.Time marshals as a string, not as its fields.
	if t == reflect.TypeOf(time.Time{}) {
		return
	}
	// Recursive types (a pattern holding patterns) would otherwise not
	// terminate. Visiting a type once per branch is enough: its fields do not
	// change.
	if seen[t] {
		return
	}
	seen[t] = true
	defer delete(seen, t)

	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag, tagged := f.Tag.Lookup("json")
		name := strings.Split(tag, ",")[0]
		if name == "-" {
			continue
		}
		// An embedded struct with no json name is inlined into the parent
		// object, so its fields are the parent's fields.
		if f.Anonymous && name == "" {
			walkWireFields(f.Type, path, seen, fn)
			continue
		}
		if !tagged || name == "" {
			// No tag: encoding/json uses the Go field name verbatim.
			name = f.Name
		}
		fn(name, path+"."+f.Name)
		walkWireFields(f.Type, path+"."+f.Name, seen, fn)
	}
}

// The three ways the interface can name a wire field. Between them they are
// deliberately generous: this test's job is to catch a field NOTHING in the UI
// mentions, so a false pass on an ambiguous name costs less than a false
// failure that trains people to edit the exemption list.
//
// The known weakness of that choice: a field whose name is an ordinary word --
// "now", "ok", "pass" -- matches something in the UI almost by accident, so
// drift in one of those goes uncaught. Names like that are worth avoiding on
// the wire for exactly this reason.
var uiNamePatterns = []*regexp.Regexp{
	// A property declaration: `rate_mbps: number;`, `loss_burst?: number;`,
	// or the quoted form `'rate_mbps': number;`.
	regexp.MustCompile(`(?m)^\s*'?"?([A-Za-z_][A-Za-z0-9_]*)"?'?\??\s*:`),
	// A string literal, as the const tables in types.ts do with
	// `key: 'reorder_pct' as const`. Naming a field that way is still naming it.
	regexp.MustCompile(`['"` + "`" + `]([A-Za-z_][A-Za-z0-9_]*)['"` + "`" + `]`),
	// A property read, which is how a .vue template reaches most fields:
	// `client.rx_duration_us`, `s.phy_down`.
	regexp.MustCompile(`\.([A-Za-z_][A-Za-z0-9_]*)`),
}

// uiFieldNames is every identifier named anywhere in the interface source.
func uiFieldNames() (map[string]bool, error) {
	names := map[string]bool{}
	err := filepath.WalkDir(filepath.FromSlash(uiSrcDir), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		switch ext := filepath.Ext(path); {
		case d.IsDir(), ext != ".ts" && ext != ".vue":
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, re := range uiNamePatterns {
			for _, m := range re.FindAllStringSubmatch(string(src), -1) {
				names[m[1]] = true
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		// An empty result means the walk found no sources, not that the UI
		// names nothing. Failing here beats passing every field vacuously.
		return nil, os.ErrInvalid
	}
	return names, nil
}
