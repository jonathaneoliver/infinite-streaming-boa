package boa

import "testing"

// The fixture is the real neighbour table from the box, where one MAC held all
// three entries at once. Before the family check the fe80:: address was parsed
// last and displaced the v4 address, which then reached tc as an IPv4 filter
// and was rejected -- taking the device's IPv6 filters down with it, because
// writeFilters returns on the first error.
func TestNeighFromEntriesKeepsV4WhenAMACAlsoHasV6(t *testing.T) {
	got := neighFromEntries([]neighJSON{
		{Dst: "192.168.0.25", LLAddr: "12:bb:19:0e:ac:7c", State: []string{"STALE"}},
		{Dst: "fdd5:a04f:f953:4412:cf8:b01b:263c:c723", LLAddr: "12:bb:19:0e:ac:7c", State: []string{"STALE"}},
		{Dst: "fe80::cf3:a992:792a:b28e", LLAddr: "12:bb:19:0e:ac:7c", State: []string{"STALE"}},
	})
	if want := "192.168.0.25"; got["12:bb:19:0e:ac:7c"] != want {
		t.Errorf("v6 displaced the v4 address: got %q, want %q",
			got["12:bb:19:0e:ac:7c"], want)
	}
}

// Ordering must not decide the answer. The kernel does not promise one, and the
// original bug was only visible because the fe80:: entry happened to come last.
func TestNeighFromEntriesIgnoresOrdering(t *testing.T) {
	got := neighFromEntries([]neighJSON{
		{Dst: "fe80::cf3:a992:792a:b28e", LLAddr: "12:bb:19:0e:ac:7c", State: []string{"STALE"}},
		{Dst: "192.168.0.25", LLAddr: "12:bb:19:0e:ac:7c", State: []string{"STALE"}},
	})
	if want := "192.168.0.25"; got["12:bb:19:0e:ac:7c"] != want {
		t.Errorf("got %q, want %q", got["12:bb:19:0e:ac:7c"], want)
	}
}

// A MAC with nothing but v6 yields no entry at all, rather than an entry that
// cannot be turned into a working filter. Absent is honest; wrong is not.
func TestNeighFromEntriesDropsV6OnlyMACs(t *testing.T) {
	got := neighFromEntries([]neighJSON{
		{Dst: "fdd5:a04f:f953:4412:81a:ed9:4fed:6ce", LLAddr: "a0:ce:c8:b6:de:53", State: []string{"REACHABLE"}},
	})
	if v, ok := got["a0:ce:c8:b6:de:53"]; ok {
		t.Errorf("v6-only MAC produced an IPv4 entry: %q", v)
	}
}

// The pre-existing reachability rule still holds: an address that did not
// answer must not carry a filter.
func TestNeighFromEntriesStillDropsUnreachable(t *testing.T) {
	got := neighFromEntries([]neighJSON{
		{Dst: "192.168.0.9", LLAddr: "aa:bb:cc:dd:ee:01", State: []string{"FAILED"}},
		{Dst: "192.168.0.10", LLAddr: "aa:bb:cc:dd:ee:02", State: []string{"INCOMPLETE"}},
		{Dst: "192.168.0.11", LLAddr: "aa:bb:cc:dd:ee:03", State: []string{"REACHABLE"}},
	})
	if len(got) != 1 || got["aa:bb:cc:dd:ee:03"] != "192.168.0.11" {
		t.Errorf("reachability filtering regressed: %v", got)
	}
}

// Garbage in the Dst field must not become a filter argument.
func TestNeighFromEntriesRejectsUnparseableAddresses(t *testing.T) {
	got := neighFromEntries([]neighJSON{
		{Dst: "not-an-address", LLAddr: "aa:bb:cc:dd:ee:04", State: []string{"REACHABLE"}},
	})
	if len(got) != 0 {
		t.Errorf("unparseable address was accepted: %v", got)
	}
}
