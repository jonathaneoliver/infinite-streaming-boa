package boa

import "testing"

// Which interface stops merging is decided by where the capped traffic
// ARRIVES: an uplink cap's packets come in on the client's own port, a
// downlink cap's on the WAN.
func TestGROPortsFollowWhereTheTrafficArrives(t *testing.T) {
	got := groPorts([]Desired{
		{Key: "a", IP: "10.0.0.2", Port: "eth2", Up: Shape{RateMbps: 1}},
		{Key: "b", IP: "10.0.0.3", Port: "phy1-ap0", Down: Shape{RateMbps: 5}},
	}, "eth0")
	want := map[string]bool{"eth2": true, "eth0": true}
	if len(got) != len(want) || !got["eth2"] || !got["eth0"] {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// The threshold is the operator's, 30 Mbps: below it the port stops merging,
// at it and above (and unlimited, 0) it keeps the CPU saving.
func TestGROStaysOnAtAndAboveTheThreshold(t *testing.T) {
	for _, rate := range []float64{0, groOffBelowMbps, 100, 900} {
		got := groPorts([]Desired{{Key: "a", IP: "10.0.0.2", Port: "eth2",
			Up: Shape{RateMbps: rate}, Down: Shape{RateMbps: rate}}}, "eth0")
		if len(got) != 0 {
			t.Errorf("%g Mbps turned GRO off on %v", rate, got)
		}
	}
	got := groPorts([]Desired{{Key: "a", IP: "10.0.0.2", Port: "eth2",
		Up: Shape{RateMbps: 29.9}}}, "eth0")
	if !got["eth2"] {
		t.Errorf("29.9 Mbps left eth2 merging")
	}
}

// A client with no address is not shaped, so it must not cost a port its GRO;
// nor can an uplink cap act on a port the bridge has not learned yet.
func TestGROIgnoresWhatIsNotShaped(t *testing.T) {
	got := groPorts([]Desired{
		{Key: "a", Port: "eth2", Up: Shape{RateMbps: 1}},
		{Key: "b", IP: "10.0.0.3", Up: Shape{RateMbps: 1}},
	}, "eth0")
	if len(got) != 0 {
		t.Errorf("got %v", got)
	}
}
