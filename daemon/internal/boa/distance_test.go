package boa

import (
	"math"
	"testing"
)

// The 1m free-space reference is the anchor for both bands, and the gap between
// them is the reason a walk ends on 2.4GHz. If this drifts, every distance the
// model reports drifts with it.
func TestFreeSpaceAtOneMetreSeparatesTheBands(t *testing.T) {
	five := freeSpaceAt1m(5745)
	two := freeSpaceAt1m(2462)
	if math.Abs(five-47.6) > 0.1 {
		t.Errorf("5745 MHz at 1m = %.2f dB, want ~47.6", five)
	}
	if math.Abs(two-40.3) > 0.1 {
		t.Errorf("2462 MHz at 1m = %.2f dB, want ~40.3", two)
	}
	if gap := five - two; math.Abs(gap-7.3) > 0.1 {
		t.Errorf("band gap = %.2f dB, want ~7.3 -- this is why 5GHz has shorter range", gap)
	}
}

// Every doubling of distance costs the same number of dB. That is what makes a
// distance control logarithmic, and a linear slider wrong.
func TestEveryDoublingCostsTheSame(t *testing.T) {
	const n = 3.0
	a := RssiAt(2, 5745, n) - RssiAt(4, 5745, n)
	b := RssiAt(8, 5745, n) - RssiAt(16, 5745, n)
	if math.Abs(a-b) > 0.01 {
		t.Errorf("2->4m cost %.2f dB but 8->16m cost %.2f dB; should be equal", a, b)
	}
	if want := 10 * n * math.Log10(2); math.Abs(a-want) > 0.01 {
		t.Errorf("a doubling cost %.2f dB, want %.2f at n=%v", a, want, n)
	}
}

func TestDistanceForInvertsRssiAt(t *testing.T) {
	for _, d := range []float64{1, 3, 7.5, 20, 60} {
		for _, f := range []int{2462, 5745} {
			got := DistanceFor(RssiAt(d, f, 3.0), f, 3.0)
			if math.Abs(got-d) > 0.01 {
				t.Errorf("round trip at %vm on %d MHz came back %vm", d, f, got)
			}
		}
	}
}

// The same metres are a WEAKER signal on 5GHz, which is the whole reason a
// client walking away ends up on 2.4GHz.
func TestFiveGigIsWeakerAtTheSameDistance(t *testing.T) {
	five := RssiAt(15, 5745, 3.0)
	two := RssiAt(15, 2462, 3.0)
	if five >= two {
		t.Fatalf("5GHz %.1f dBm should be weaker than 2.4GHz %.1f dBm at 15m", five, two)
	}
	if gap := two - five; math.Abs(gap-7.3) > 0.2 {
		t.Errorf("gap at equal distance = %.2f dB, want the 1m free-space difference ~7.3", gap)
	}
}

/*
 * THE CLIFF.
 *
 * This is the property most likely to be smoothed away by someone making the
 * control "feel better", so it is pinned explicitly: most of the range must be
 * nearly untouched, and the collapse must happen inside a narrow band of dB.
 */
func TestDegradationIsACliffNotASlope(t *testing.T) {
	const f, w = 5745, 80
	strong, _ := ShapeForLevels(-45, (-45)-DefaultTxDb, f, w)
	mid, _ := ShapeForLevels(-60, (-60)-DefaultTxDb, f, w)
	weak, _ := ShapeForLevels(-73, (-73)-DefaultTxDb, f, w)

	ceiling := phyCeilingMbps(f, w)
	if strong.RateMbps < ceiling*0.9 {
		t.Errorf("a strong link should be near the ceiling, got %v of %v", strong.RateMbps, ceiling)
	}
	if strong.LossPct != 0 || strong.CorruptPct != 0 {
		t.Errorf("a strong link must impose nothing, got loss %v corrupt %v",
			strong.LossPct, strong.CorruptPct)
	}
	// The top half of the range should still be broadly healthy...
	if mid.RateMbps < ceiling*0.4 {
		t.Errorf("mid-range collapsed too early: %v of %v", mid.RateMbps, ceiling)
	}
	// ...and then the last stretch should fall hard.
	if weak.RateMbps > mid.RateMbps*0.5 {
		t.Errorf("no cliff: -73 gave %v against -60's %v", weak.RateMbps, mid.RateMbps)
	}
}

/*
 * Corrupt leads, loss follows late.
 *
 * A weak signal damages frames that fail FCS having already spent the airtime;
 * it does not drop IP packets. 802.11 retries hide loss from the application
 * until they are exhausted. A model that raises loss first is modelling a lossy
 * WAN, not a weakening radio.
 */
func TestCorruptArrivesBeforeLoss(t *testing.T) {
	const f, w = 5745, 80
	var firstCorrupt, firstLoss float64
	for dbm := -40.0; dbm > -85; dbm -= 0.5 {
		d, _ := ShapeForLevels(dbm, (dbm)-DefaultTxDb, f, w)
		if firstCorrupt == 0 && d.CorruptPct > 0 {
			firstCorrupt = dbm
		}
		if firstLoss == 0 && d.LossPct > 0 {
			firstLoss = dbm
		}
	}
	if firstCorrupt == 0 {
		t.Fatal("corruption never appeared across the whole range")
	}
	if firstLoss == 0 {
		t.Fatal("loss never appeared across the whole range")
	}
	if firstLoss >= firstCorrupt {
		t.Errorf("loss appeared at %v dBm, at or before corruption at %v dBm; "+
			"retries should hide loss until well after frames start failing",
			firstLoss, firstCorrupt)
	}
}

// Loss without a burst length is a per-packet coin flip, which essentially
// never happens on a real link.
func TestLossIsAlwaysBursty(t *testing.T) {
	for dbm := -40.0; dbm > -90; dbm -= 0.5 {
		d, _ := ShapeForLevels(dbm, (dbm)-DefaultTxDb, 5745, 80)
		if d.LossPct > 0 && d.LossBurst <= 1 {
			t.Fatalf("at %v dBm loss %v came with burst %v; correlated loss needs burst > 1",
				dbm, d.LossPct, d.LossBurst)
		}
	}
}

// Rate must fall monotonically as signal falls. A non-monotonic curve would make
// the slider jump around under the operator's hand.
func TestRateFallsMonotonically(t *testing.T) {
	prev := math.Inf(1)
	for dbm := -40.0; dbm > -82; dbm -= 0.5 {
		d, _ := ShapeForLevels(dbm, (dbm)-DefaultTxDb, 5745, 80)
		if d.RateMbps > prev+0.001 {
			t.Fatalf("rate rose as signal fell: %v at %v dBm after %v", d.RateMbps, dbm, prev)
		}
		prev = d.RateMbps
	}
}

// A wider channel needs a stronger signal for the same result, so at one level
// the narrow link should be the healthier of the two.
func TestWiderChannelsDieFirst(t *testing.T) {
	wide, _ := ShapeForLevels(-70, (-70)-DefaultTxDb, 5745, 80)
	narrow, _ := ShapeForLevels(-70, (-70)-DefaultTxDb, 5745, 20)
	if narrow.CorruptPct > wide.CorruptPct {
		t.Errorf("20MHz should be more robust than 80MHz at the same level: "+
			"corrupt %v vs %v", narrow.CorruptPct, wide.CorruptPct)
	}
}

/*
 * Out of range is total loss, NOT a zero rate.
 *
 * Zero means "unlimited" everywhere else in this codebase (see Shape.RateMbps),
 * so returning a rate of zero at the floor would read as "no cap" and hand the
 * client a perfect link at the exact moment it should have none.
 */
func TestOutOfRangeIsLossNotAnUnlimitedRate(t *testing.T) {
	d, u := ShapeForLevels(-95, (-95)-DefaultTxDb, 5745, 80)
	if d.LossPct != 100 {
		t.Errorf("beyond the floor should be total loss, got %v", d.LossPct)
	}
	if d.RateMbps != 0 {
		t.Errorf("expected no rate cap alongside total loss, got %v", d.RateMbps)
	}
	if u.LossPct != 100 {
		t.Errorf("uplink should be gone too, got %v", u.LossPct)
	}
}

/*
 * Uplink fails FIRST, because the client is the quieter transmitter.
 *
 * This is the direction of the asymmetry, and it is easy to get backwards: an
 * earlier version scaled uplink to a fraction of downlink's rate, which models
 * an ISP's asymmetric plan rather than distance, and gave uplink a smaller pipe
 * on an equally good link instead of an equally sized pipe on a worse one.
 */
func TestUplinkDegradesBeforeDownlink(t *testing.T) {
	const f, w = 5745, 80
	// Somewhere in the middle of the cliff, where both are alive but unequal.
	d, u := ShapeForLevels(-68, (-68)-DefaultTxDb, f, w)
	if u.RateMbps >= d.RateMbps {
		t.Errorf("uplink %v should be slower than downlink %v at the same distance",
			u.RateMbps, d.RateMbps)
	}
	if u.CorruptPct <= d.CorruptPct {
		t.Errorf("uplink should be the more damaged direction: corrupt %v vs %v",
			u.CorruptPct, d.CorruptPct)
	}

	// And it should reach the floor sooner: a level exists where the client can
	// still hear the AP but can no longer be heard.
	var found bool
	for dbm := -60.0; dbm > -85; dbm -= 0.5 {
		dd, uu := ShapeForLevels(dbm, (dbm)-DefaultTxDb, f, w)
		if uu.LossPct == 100 && dd.LossPct < 100 {
			found = true
			break
		}
	}
	if !found {
		t.Error("no level where uplink is gone but downlink survives; " +
			"the client should lose the ability to be heard first")
	}
}

/*
 * The delta is the only thing separating the two directions, and zero means
 * they are the same link.
 *
 * Zero has to be expressible: "both directions equally loud" is a legitimate
 * thing to ask for, which is why DeltaDb is not omitempty -- an absent field
 * would be indistinguishable from someone asking for it.
 */
func TestEqualLevelsGiveEqualDirections(t *testing.T) {
	const f, w = 5745, 80
	d, u := ShapeForLevels(-68, -68, f, w)
	if d != u {
		t.Errorf("at equal levels the directions should be identical:\n down %+v\n up   %+v", d, u)
	}

	// And a louder client is a better uplink at the same distance.
	_, quiet := ShapeForLevels(-68, (-68)-12, f, w)
	_, loud := ShapeForLevels(-68, (-68)-2, f, w)
	if loud.RateMbps <= quiet.RateMbps {
		t.Errorf("a louder client should get the better uplink: %v at 2dB vs %v at 12dB",
			loud.RateMbps, quiet.RateMbps)
	}
}

/*
 * THE ANTENNA MOVES BOTH DIRECTIONS; THE TRANSMITTER MOVES ONLY ONE.
 *
 * Antenna gain is reciprocal -- the same small antenna that transmits poorly
 * also receives poorly -- so a worse device hears worse as well as being heard
 * worse. An earlier version had only the transmit half, which left downlink
 * untouched when the device kind changed, and that was simply wrong physics.
 */
func TestTheAntennaIsReciprocalAndTheTransmitterIsNot(t *testing.T) {
	const path = -60.0

	laptop := RssiModel{Dbm: path, RxDb: 0, TxDb: 3}
	watch := RssiModel{Dbm: path, RxDb: 5, TxDb: 6}

	lDown, lUp := LevelsFor(path, laptop)
	wDown, wUp := LevelsFor(path, watch)

	if wDown >= lDown {
		t.Errorf("a worse antenna must also HEAR worse: watch %v vs laptop %v", wDown, lDown)
	}
	if wUp >= lUp {
		t.Errorf("and be heard worse: watch %v vs laptop %v", wUp, lUp)
	}
	// The antenna is subtracted once per direction; the transmitter only from up.
	if lDown != path-laptop.RxDb {
		t.Errorf("downlink should be path minus antenna only, got %v", lDown)
	}
	if lUp != path-laptop.RxDb-laptop.TxDb {
		t.Errorf("uplink should be path minus antenna and transmitter, got %v", lUp)
	}

	// A device with no losses of its own sits exactly on the path.
	ideal := RssiModel{Dbm: path}
	if d, u := LevelsFor(path, ideal); d != path || u != path {
		t.Errorf("an ideal device should sit on the path, got down %v up %v", d, u)
	}
}

// Both directions cross the SAME channel, so a wider channel or a different
// band moves them together. Only the transmit power differs.
func TestBothDirectionsShareTheChannel(t *testing.T) {
	narrowD, narrowU := ShapeForLevels(-60, (-60)-DefaultTxDb, 2462, 20)
	wideD, wideU := ShapeForLevels(-60, (-60)-DefaultTxDb, 5745, 80)
	if !(wideD.RateMbps > narrowD.RateMbps) {
		t.Errorf("80MHz downlink should out-run 20MHz: %v vs %v",
			wideD.RateMbps, narrowD.RateMbps)
	}
	if !(wideU.RateMbps > narrowU.RateMbps) {
		t.Errorf("uplink should follow the same channel: %v vs %v",
			wideU.RateMbps, narrowU.RateMbps)
	}
}

// A strong link must be indistinguishable from no conditioning at all.
func TestAStrongLinkIsClean(t *testing.T) {
	d, u := ShapeForLevels(-38, (-38)-DefaultTxDb, 5745, 80)
	if d.DelayMs > 5 || d.LossPct != 0 || d.CorruptPct != 0 {
		t.Errorf("a strong link should be near-clean, got %+v", d)
	}
	if u.RateMbps <= 0 {
		t.Errorf("uplink should still have a cap, got %v", u.RateMbps)
	}
}

func TestFreqForChannelMirrorsChannelForFreq(t *testing.T) {
	for _, ch := range []int{1, 6, 11, 14, 36, 40, 44, 48, 149, 153, 157, 161, 165} {
		f := freqForChannel(ch)
		if f == 0 {
			t.Errorf("channel %d has no frequency", ch)
			continue
		}
		if back := channelForFreq(f); back != ch {
			t.Errorf("channel %d -> %d MHz -> channel %d", ch, f, back)
		}
	}
}

/*
 * The model must drive the kernel WITHOUT being written to the store.
 *
 * This is the property that keeps the input and its derived output from ever
 * disagreeing: only one of them exists on disk. If a future change starts
 * persisting the derived shapes, this test is what should stop it.
 */
func TestTheModelDrivesTheKernelWithoutTouchingTheStore(t *testing.T) {
	e := &Engine{cfg: Config{WlanPorts: []string{"wlan-usb"}},
		sweep: &Sweeper{}, player: &Player{}}
	c := Client{
		MAC: "aa:bb:cc:dd:ee:ff", IP: "192.168.0.50", Present: true,
		RadioOn: &RadioOn{Iface: "wlan-usb", Channel: 149, WidthMHz: 80},
		Policy: Policy{
			MAC: "aa:bb:cc:dd:ee:ff", Enabled: true,
			// Deliberately NOT what the model implies, so an applied 999 proves
			// the stored value won where it should not have.
			Down: Shape{RateMbps: 999},
			Up:   Shape{RateMbps: 999},
			Rssi: &RssiModel{Dbm: -74, N: DefaultExponent},
		},
	}

	var dev *Desired
	got := e.desired([]Client{c})
	for i := range got {
		if !got[i].IsSub && got[i].Key == c.MAC {
			dev = &got[i]
		}
	}
	if dev == nil {
		t.Fatal("no device class was produced")
	}
	if dev.Down.RateMbps == 999 {
		t.Error("the stored rate was applied; the model should have replaced it")
	}
	if dev.Down.RateMbps <= 0 {
		t.Errorf("expected a derived rate, got %v", dev.Down.RateMbps)
	}
	if dev.Down.CorruptPct <= 0 {
		t.Errorf("expected corruption at -74 dBm on 80MHz, got %v", dev.Down.CorruptPct)
	}
	if c.Policy.Down.RateMbps != 999 || c.Policy.Up.RateMbps != 999 {
		t.Error("desired() mutated the stored policy; it must derive in memory only")
	}
	if c.Policy.Rssi == nil || c.Policy.Rssi.Dbm != -74 {
		t.Error("the stored model was altered")
	}
}

// A disabled device is not conditioned at all, model or no model. "Disabled"
// means do not condition, not do not measure.
func TestADisabledDeviceIgnoresTheModel(t *testing.T) {
	e := &Engine{cfg: Config{WlanPorts: []string{"wlan-usb"}},
		sweep: &Sweeper{}, player: &Player{}}
	c := Client{
		MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.0.51", Present: true,
		RadioOn: &RadioOn{Iface: "wlan-usb", Channel: 149, WidthMHz: 80},
		Policy: Policy{
			MAC: "aa:bb:cc:dd:ee:01", Enabled: false,
			Rssi: &RssiModel{Dbm: -80, N: DefaultExponent},
		},
	}
	for _, d := range e.desired([]Client{c}) {
		if d.Key != c.MAC {
			continue
		}
		if !d.Down.IsClean() || !d.Up.IsClean() {
			t.Errorf("a disabled device was conditioned: %+v / %+v", d.Down, d.Up)
		}
	}
}

/*
 * The client's OWN radio decides which curve it gets.
 *
 * -76 dBm is chosen because it straddles the two floors: it is exactly the
 * sensitivity floor for an 80MHz channel and still 6 dB of margin for a 20MHz
 * one. So the same modelled level must leave the wide link dead and the narrow
 * link working -- which is both the width rule and proof that RadioOn is being
 * consulted at all rather than a default being applied to everyone.
 */
func TestTheBandComesFromTheClientsOwnRadio(t *testing.T) {
	e := &Engine{cfg: Config{WlanPorts: []string{"wlan-usb", "wlan0"}},
		sweep: &Sweeper{}, player: &Player{}}
	shapeFor := func(mac string, ch, width int) Shape {
		c := Client{
			MAC: mac, IP: "192.168.0.60", Present: true,
			RadioOn: &RadioOn{Channel: ch, WidthMHz: width},
			Policy: Policy{
				MAC: mac, Enabled: true,
				Rssi: &RssiModel{Dbm: -76, N: DefaultExponent},
			},
		}
		for _, d := range e.desired([]Client{c}) {
			if d.Key == mac {
				return d.Down
			}
		}
		t.Fatal("no class produced")
		return Shape{}
	}
	wide := shapeFor("aa:bb:cc:dd:ee:02", 149, 80)
	narrow := shapeFor("aa:bb:cc:dd:ee:03", 11, 20)

	if wide.LossPct != 100 {
		t.Errorf("80MHz at its floor should be out of range, got loss %v", wide.LossPct)
	}
	if narrow.LossPct == 100 {
		t.Error("20MHz still has 6 dB of margin at -76 and should not be out of range")
	}
	if narrow.RateMbps <= 0 {
		t.Errorf("the narrow link should still be passing traffic, got %v", narrow.RateMbps)
	}
}
