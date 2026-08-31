package pifi

import (
	"path/filepath"
	"strings"
	"testing"
)

func devPolicy(mac string, down float64) Policy {
	return Policy{MAC: mac, Enabled: true, Down: Shape{RateMbps: down}}
}

func measuredLadder() Ladder {
	return Ladder{
		Service:    "infinite-stream",
		Provenance: LadderMeasured,
		Rungs: []Rung{
			{Mbps: 0.25, UpAtMbps: 0.45},
			{Mbps: 4.59, UpAtMbps: 6.41},
		},
	}
}

// A restore is not a hand edit. Every other write path demotes a ladder to
// "typed" so that the authority of a measurement is never attached to a number
// nobody measured -- but demoting on import would mean a box can never be
// returned to the state it was backed up from.
func TestImportPreservesLadderProvenance(t *testing.T) {
	p := devPolicy("aa:aa:aa:aa:aa:aa", 0)
	p.Ladders = []Ladder{measuredLadder()}
	// A version 1 document, which is what carried devices. Import still reads
	// them so an older backup restores what it can.
	doc := ConfigExport{Version: 1, Devices: []Policy{p}}

	next, _, _ := doc.Apply(map[string]Policy{}, ImportMerge)
	got := next["aa:aa:aa:aa:aa:aa"].Ladders[0]
	if got.Provenance != LadderMeasured {
		t.Fatalf("provenance %q after import, want %q",
			got.Provenance, LadderMeasured)
	}
	if got.Rungs[0].UpAtMbps != 0.45 {
		t.Fatalf("up_at lost: %v", got.Rungs)
	}
}

// Merge is the default because it cannot destroy configuration the operator
// did not mention.
func TestMergeLeavesUnmentionedDevicesAlone(t *testing.T) {
	cur := map[string]Policy{
		"aa:aa:aa:aa:aa:aa": devPolicy("aa:aa:aa:aa:aa:aa", 1),
		"bb:bb:bb:bb:bb:bb": devPolicy("bb:bb:bb:bb:bb:bb", 2),
	}
	doc := ConfigExport{Version: ConfigVersion, Devices: []Policy{
		devPolicy("aa:aa:aa:aa:aa:aa", 9),
	}}
	next, wrote, removed := doc.Apply(cur, ImportMerge)
	if len(next) != 2 {
		t.Fatalf("got %d devices, want 2", len(next))
	}
	if next["bb:bb:bb:bb:bb:bb"].Down.RateMbps != 2 {
		t.Fatal("merge disturbed a device the document never mentioned")
	}
	if next["aa:aa:aa:aa:aa:aa"].Down.RateMbps != 9 {
		t.Fatal("merge did not apply the document")
	}
	if len(wrote) != 1 || len(removed) != 0 {
		t.Fatalf("wrote %v removed %v", wrote, removed)
	}
}

func TestReplaceRemovesDevicesAbsentFromTheDocument(t *testing.T) {
	cur := map[string]Policy{
		"aa:aa:aa:aa:aa:aa": devPolicy("aa:aa:aa:aa:aa:aa", 1),
		"bb:bb:bb:bb:bb:bb": devPolicy("bb:bb:bb:bb:bb:bb", 2),
	}
	doc := ConfigExport{Version: ConfigVersion, Devices: []Policy{
		devPolicy("aa:aa:aa:aa:aa:aa", 9),
	}}
	next, _, removed := doc.Apply(cur, ImportReplace)
	if len(next) != 1 {
		t.Fatalf("got %d devices, want 1", len(next))
	}
	if len(removed) != 1 || removed[0] != "bb:bb:bb:bb:bb:bb" {
		t.Fatalf("removed %v", removed)
	}
}

// Importing must not reset another client's view of how many times a device has
// changed, or its next write would be accepted against a revision it never saw.
func TestImportAdvancesTheRevisionCounter(t *testing.T) {
	cur := map[string]Policy{"aa:aa:aa:aa:aa:aa": {
		MAC: "aa:aa:aa:aa:aa:aa", Rev: 12, Enabled: true,
	}}
	doc := ConfigExport{Version: ConfigVersion, Devices: []Policy{
		devPolicy("aa:aa:aa:aa:aa:aa", 5),
	}}
	next, _, _ := doc.Apply(cur, ImportMerge)
	if got := next["aa:aa:aa:aa:aa:aa"].Rev; got != 13 {
		t.Fatalf("rev %d after import, want 13", got)
	}
}

func TestValidateRejectsBadDocuments(t *testing.T) {
	bad := []struct {
		name string
		doc  ConfigExport
	}{
		{"wrong version", ConfigExport{Version: 99, Devices: []Policy{
			devPolicy("aa:aa:aa:aa:aa:aa", 1)}}},
		{"no devices", ConfigExport{Version: ConfigVersion}},
		{"no mac", ConfigExport{Version: ConfigVersion,
			Devices: []Policy{devPolicy("", 1)}}},
		{"duplicate mac", ConfigExport{Version: ConfigVersion, Devices: []Policy{
			devPolicy("aa:aa:aa:aa:aa:aa", 1),
			devPolicy("AA:AA:AA:AA:AA:AA", 2)}}},
		{"negative rate", ConfigExport{Version: ConfigVersion,
			Devices: []Policy{devPolicy("aa:aa:aa:aa:aa:aa", -1)}}},
		{"ladder with no rungs", ConfigExport{Version: ConfigVersion,
			Devices: []Policy{{MAC: "aa:aa:aa:aa:aa:aa", Enabled: true,
				Ladders: []Ladder{{Service: "x"}}}}}},
		{"ladder with no service", ConfigExport{Version: ConfigVersion,
			Devices: []Policy{{MAC: "aa:aa:aa:aa:aa:aa", Enabled: true,
				Ladders: []Ladder{{Rungs: []Rung{{Mbps: 1}}}}}}}},
		{"rung at zero", ConfigExport{Version: ConfigVersion,
			Devices: []Policy{{MAC: "aa:aa:aa:aa:aa:aa", Enabled: true,
				Ladders: []Ladder{{Service: "x", Rungs: []Rung{{Mbps: 0}}}}}}}},
	}
	for _, c := range bad {
		if err := c.doc.Validate(); err == nil {
			t.Errorf("%s: accepted, want rejected", c.name)
		}
	}
}

// A configuration the box produced must be one the box accepts. Round-tripping
// is the whole point: the export exists so a reflashed appliance can be put
// back the way it was.
func TestExportRoundTripsThroughImport(t *testing.T) {
	p := devPolicy("aa:aa:aa:aa:aa:aa", 12)
	p.Label = "jonathan's iphone"
	p.Ladders = []Ladder{measuredLadder()}
	p.Pattern = &Pattern{Keys: []Keyframe{
		kf(0, 12, EaseHold), kf(30, 1.5, EaseHold)}, Loop: true}
	doc := ConfigExport{Version: 1, Devices: []Policy{p}}
	if err := doc.Validate(); err != nil {
		t.Fatalf("the box rejected its own export: %v", err)
	}
	next, wrote, _ := doc.Apply(map[string]Policy{}, ImportMerge)
	if len(wrote) != 1 {
		t.Fatalf("wrote %v", wrote)
	}
	got := next["aa:aa:aa:aa:aa:aa"]
	if got.Label != "jonathan's iphone" || got.Down.RateMbps != 12 {
		t.Fatalf("policy not restored: %+v", got)
	}
	if got.Pattern == nil || len(got.Pattern.Keys) != 2 || !got.Pattern.Loop {
		t.Fatalf("pattern not restored: %+v", got.Pattern)
	}
	if len(got.Ladders) != 1 || len(got.Ladders[0].Rungs) != 2 {
		t.Fatalf("ladder not restored: %+v", got.Ladders)
	}
}

// A failed write must leave memory agreeing with disk, or the box reports a
// configuration it is not conditioning by.
func TestReplaceAllKeepsMemoryAndDiskTogetherOnFailure(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(filepath.Join(dir, "policy.json"))
	if err := s.Put(devPolicy("aa:aa:aa:aa:aa:aa", 1)); err != nil {
		t.Fatal(err)
	}
	// A directory where the file should be: save() cannot write it.
	s.path = dir
	err := s.ReplaceAll(map[string]Policy{
		"bb:bb:bb:bb:bb:bb": devPolicy("bb:bb:bb:bb:bb:bb", 2),
	})
	if err == nil {
		t.Fatal("ReplaceAll succeeded writing to a directory")
	}
	if _, ok := s.Get("aa:aa:aa:aa:aa:aa"); !ok {
		t.Fatal("rolled forward in memory after a failed write")
	}
	if _, ok := s.Get("bb:bb:bb:bb:bb:bb"); ok {
		t.Fatal("kept the failed import in memory")
	}
}

// A document from an older schema still imports. This is the whole point of
// versioning it: adding a field must not make every file already saved
// unreadable, or the format is only good until it changes once.
func TestValidateAcceptsOlderVersions(t *testing.T) {
	c := ConfigExport{Version: 1, Devices: []Policy{{MAC: "aa:bb:cc:dd:ee:ff"}}}
	if err := c.Validate(); err != nil {
		t.Fatalf("a version 1 document was refused: %v", err)
	}
}

// A document from a NEWER schema is refused, and that asymmetry is deliberate:
// this code has already understood the past, but would only half-understand the
// future, and half-understanding means conditioning traffic by a policy the
// operator did not describe.
func TestValidateRefusesNewerVersions(t *testing.T) {
	c := ConfigExport{Version: ConfigVersion + 1, Devices: []Policy{{MAC: "aa:bb:cc:dd:ee:ff"}}}
	err := c.Validate()
	if err == nil {
		t.Fatal("a future document was accepted")
	}
	if !strings.Contains(err.Error(), "newer than this box understands") {
		t.Errorf("the refusal does not say why: %v", err)
	}
	// And a missing version is not a very old document, it is not a document.
	if err := (ConfigExport{Devices: []Policy{{MAC: "aa:bb:cc:dd:ee:ff"}}}).Validate(); err == nil {
		t.Error("a document with no version was accepted")
	}
}

// A pattern library with no devices is a valid document. Patterns are not
// device-specific -- keyed by name, and generated from one ladder for the whole
// box -- so a file carrying only patterns is the natural unit to send someone.
func TestValidateAcceptsAPatternLibraryWithNoDevices(t *testing.T) {
	c := ConfigExport{
		Version: ConfigVersion,
		Patterns: []Pattern{{
			Name: "shared", Loop: true,
			Keys: []Keyframe{
				{AtSec: 0, Down: Shape{RateMbps: 5}, Ease: EaseHold},
				{AtSec: 30, Down: Shape{RateMbps: 1}, Ease: EaseHold},
				{AtSec: 60, Down: Shape{RateMbps: 5}, Ease: EaseHold},
			},
		}},
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("a pattern-only document was refused: %v", err)
	}
}

// Empty of both is still refused. It is not a configuration, and in replace
// mode applying it would delete every device to honour a document that says
// nothing.
func TestValidateRefusesADocumentThatSaysNothing(t *testing.T) {
	if err := (ConfigExport{Version: ConfigVersion}).Validate(); err == nil {
		t.Error("an empty document was accepted")
	}
}

// Version 2 exports no devices. Half of a version 1 document was a MAC, a
// label, a revision counter belonging to one box's edit history, working state
// an operator re-sets per test, and a baked copy of each device's loaded
// pattern duplicating the library. None of it restores anywhere else.
func TestExportCarriesNoDevices(t *testing.T) {
	l := measuredLadder()
	doc := ExportConfig(&l, []Pattern{{Name: "mine", Keys: []Keyframe{
		kf(0, 8, EaseHold), kf(30, 2, EaseHold)}}})
	if len(doc.Devices) != 0 {
		t.Errorf("exported %d device(s); version 2 carries none", len(doc.Devices))
	}
	if doc.Version != 2 {
		t.Errorf("version %d, want 2", doc.Version)
	}
	if doc.Ladder == nil {
		t.Fatal("the ladder was not exported, which is the one expensive thing here")
	}
	if len(doc.Patterns) != 1 {
		t.Errorf("patterns %v", doc.Patterns)
	}
	if err := doc.Validate(); err != nil {
		t.Fatalf("the box rejected its own export: %v", err)
	}
}

// A ladder alone is a valid document: it is the hour of real streaming, and the
// thing most worth carrying off the box before a reflash.
func TestExportOfALadderAloneIsValid(t *testing.T) {
	l := measuredLadder()
	doc := ExportConfig(&l, nil)
	if err := doc.Validate(); err != nil {
		t.Fatalf("a ladder-only document was refused: %v", err)
	}
}

// A ladder too short to walk is refused rather than stored, because it cannot
// drive a pattern and would shadow a usable one.
func TestValidateRefusesAnUnusableLadder(t *testing.T) {
	doc := ConfigExport{Version: ConfigVersion, Ladder: &Ladder{
		Service: "x", Rungs: []Rung{{Mbps: 1, UpAtMbps: 1.5}}}}
	if err := doc.Validate(); err == nil {
		t.Error("a one-rung ladder was accepted")
	}
}

// The box's ladder wins over anything still recorded under a device, and the
// device scan remains as the migration path for a box swept before the move.
func TestGlobalLadderPrefersTheBoxOverADevice(t *testing.T) {
	deviceLadder := Ladder{Service: "old", Provenance: LadderMeasured, MeasuredAt: 999,
		Rungs: []Rung{{Mbps: 1, UpAtMbps: 1.5}, {Mbps: 2, UpAtMbps: 3}}}
	boxLadder := Ladder{Service: "box", Provenance: LadderMeasured, MeasuredAt: 1,
		Rungs: []Rung{{Mbps: 4, UpAtMbps: 6}, {Mbps: 8, UpAtMbps: 12}}}
	all := map[string]Policy{"aa:bb": {Ladders: []Ladder{deviceLadder}}}

	got, ok := GlobalLadder(boxLadder, true, all)
	if !ok || got.Service != "box" {
		t.Errorf("got %q, want the box's ladder even though the device's is newer", got.Service)
	}
	// Nothing stored: fall back to the device, so an old box keeps working
	// without a re-sweep.
	got, ok = GlobalLadder(Ladder{}, false, all)
	if !ok || got.Service != "old" {
		t.Errorf("migration path broken: got %q", got.Service)
	}
}

// Importing a pattern library in merge mode must not destroy the one already
// there. It used to: the library was replaced wholesale in both modes, which
// was defensible while this document was a whole-box backup and stopped being
// so the moment a patterns-only document became the way to send someone a
// pattern. Replace mode still makes the library match the document.
func TestPatternImportModeIsHonoured(t *testing.T) {
	two := []Keyframe{kf(0, 8, EaseHold), kf(30, 2, EaseHold)}
	mine := Pattern{Name: "mine", Keys: two, Loop: true}
	theirs := Pattern{Name: "theirs", Keys: two, Loop: true}

	dir := t.TempDir()
	st := NewPatternStore(filepath.Join(dir, "patterns.json"))
	if err := st.Put(mine); err != nil {
		t.Fatal(err)
	}
	// Merge: theirs arrives, mine survives.
	if err := st.Put(theirs); err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, p := range st.All() {
		names[p.Name] = true
	}
	if !names["mine"] || !names["theirs"] {
		t.Fatalf("merge lost a pattern: %v", names)
	}
	// Replace: the library becomes exactly the document.
	if err := st.ReplaceAll([]Pattern{theirs}); err != nil {
		t.Fatal(err)
	}
	names = map[string]bool{}
	for _, p := range st.All() {
		names[p.Name] = true
	}
	if names["mine"] || !names["theirs"] {
		t.Fatalf("replace did not make the library match: %v", names)
	}
}
