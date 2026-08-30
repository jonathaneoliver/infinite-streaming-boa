package pifi

import (
	"path/filepath"
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

// Export must be byte-stable across runs. These documents are meant to be
// committed and diffed, and Go randomises map iteration -- so an unchanged
// configuration exporting in a different order every time would make the diff
// useless for the one job it has.
func TestExportOrdersDevicesByMAC(t *testing.T) {
	all := map[string]Policy{
		"cc:cc:cc:cc:cc:cc": devPolicy("cc:cc:cc:cc:cc:cc", 3),
		"aa:aa:aa:aa:aa:aa": devPolicy("aa:aa:aa:aa:aa:aa", 1),
		"bb:bb:bb:bb:bb:bb": devPolicy("bb:bb:bb:bb:bb:bb", 2),
	}
	for i := 0; i < 8; i++ {
		got := ExportConfig(all)
		if len(got.Devices) != 3 {
			t.Fatalf("got %d devices, want 3", len(got.Devices))
		}
		if got.Devices[0].MAC != "aa:aa:aa:aa:aa:aa" ||
			got.Devices[1].MAC != "bb:bb:bb:bb:bb:bb" ||
			got.Devices[2].MAC != "cc:cc:cc:cc:cc:cc" {
			t.Fatalf("run %d: out of order: %v", i, macsOf(got.Devices))
		}
	}
}

func macsOf(ps []Policy) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.MAC)
	}
	return out
}

// Rev belongs to one box's edit history. Carrying it into a document invites a
// restore to write a revision from a different timeline, and churns the diff of
// a committed file on every unrelated edit.
func TestExportDropsTheRevisionCounter(t *testing.T) {
	all := map[string]Policy{"aa:aa:aa:aa:aa:aa": {
		MAC: "aa:aa:aa:aa:aa:aa", Rev: 47, Enabled: true,
	}}
	if got := ExportConfig(all).Devices[0].Rev; got != 0 {
		t.Fatalf("exported rev %d, want 0", got)
	}
}

// A restore is not a hand edit. Every other write path demotes a ladder to
// "typed" so that the authority of a measurement is never attached to a number
// nobody measured -- but demoting on import would mean a box can never be
// returned to the state it was backed up from.
func TestImportPreservesLadderProvenance(t *testing.T) {
	p := devPolicy("aa:aa:aa:aa:aa:aa", 0)
	p.Ladders = []Ladder{measuredLadder()}
	doc := ExportConfig(map[string]Policy{p.MAC: p})

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
	orig := map[string]Policy{p.MAC: p}

	doc := ExportConfig(orig)
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
