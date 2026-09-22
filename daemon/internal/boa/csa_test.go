package boa

import (
	"fmt"
	"strings"
	"testing"
)

// The driver that refuses the verb it advertises is known before anybody tries,
// and the one measured doing it properly must not be on the list.
func TestTheDriverThatRefusesAnnouncementIsNamed(t *testing.T) {
	if _, bad := csaRefusedBy["mt7921u"]; !bad {
		t.Fatal("mt7921u must be listed: it refuses every form of CHAN_SWITCH (#154)")
	}
	if _, bad := csaRefusedBy["mt798x-wmac"]; bad {
		t.Fatal("mt798x-wmac was MEASURED announcing 8 switches with 23 of 24 " +
			"client-crossings kept, and must stay on the seamless path")
	}
}

// A learned refusal covers every radio on that driver, and dies with the
// daemon. Same shape and same reasoning as the transmit-power one: the claim is
// about driver code, and driver code gets fixed.
func TestALearnedCSARefusalCoversTheDriverAndIsNotPersisted(t *testing.T) {
	e := &Engine{}
	if got := e.csaRefusedReason("mt798x-wmac"); got != "" {
		t.Fatalf("a driver nobody has caught must start announceable, got %q", got)
	}
	e.noteCSARefused("mt798x-wmac", "refused an in-band move that then worked by restart")
	if got := e.csaRefusedReason("mt798x-wmac"); got == "" {
		t.Fatal("a driver caught refusing an ordinary switch must be remembered")
	}
	if (&Engine{}).csaRefusedReason("mt798x-wmac") != "" {
		t.Fatal("a learned refusal must not outlive the daemon")
	}
	if (&Engine{}).csaRefusedReason("mt7921u") == "" {
		t.Fatal("mt7921u is known before anyone tries (#154)")
	}
}

// THE TRAP, and the whole reason learnCSARefusal exists rather than a line at
// the call site.
//
// FAIL from hostapd means "this driver refuses CSA" or "this target is not
// switchable", and the two are indistinguishable at the moment it arrives. The
// map is keyed by DRIVER, so recording a target-specific refusal as a verdict
// would condemn every radio on that driver until the daemon restarts -- from
// one request that was never announceable by anybody.
//
// A cross-band move is the live example: no access point can announce its way
// from 2.4GHz to 5GHz, and asking it to is an ordinary thing for an operator to
// do from the band plan.
func TestOnlyAnInBandFailureCondemnsTheDriver(t *testing.T) {
	boom := fmt.Errorf("hostapd refused CHAN_SWITCH")

	// 2.4GHz to 5GHz: the target explains the failure, so nothing is learned
	// and the next in-band move on this radio asks again.
	crossing := &Engine{}
	crossing.learnCSARefusal("phy0-ap0", "mt798x-wmac", 1, apChannels[149], boom)
	if why := crossing.csaRefusedReason("mt798x-wmac"); why != "" {
		t.Fatalf("a cross-band move must teach nothing about the driver, learned %q", why)
	}

	// And where the radio was is not known at all -- STATUS unreadable, so
	// wasChannel is 0 -- there is no proof either, and a guess here is the
	// same damage.
	blind := &Engine{}
	blind.learnCSARefusal("phy1-ap0", "mt798x-wmac", 0, apChannels[149], boom)
	if why := blind.csaRefusedReason("mt798x-wmac"); why != "" {
		t.Fatalf("an unknown starting channel proves nothing, learned %q", why)
	}

	// 40 to 149, both 5GHz and both non-DFS by construction: the fallback then
	// landed on exactly that channel, so the target was legal and the driver is
	// the only remaining explanation.
	caught := &Engine{}
	caught.learnCSARefusal("phy1-ap0", "mt798x-wmac", 40, apChannels[149], boom)
	why := caught.csaRefusedReason("mt798x-wmac")
	if why == "" {
		t.Fatal("an in-band refusal the restart then satisfied must be learned")
	}
	// The reason is read by an operator looking at a disabled seamless badge,
	// so it names the move that proved it rather than only the driver.
	for _, want := range []string{"mt798x-wmac", "40", "149"} {
		if !strings.Contains(why, want) {
			t.Errorf("the learned reason %q does not say %q", why, want)
		}
	}
}

// The badge is drawn from the same fact the move itself consults, so a radio
// cannot advertise one behaviour and perform the other.
func TestTheBadgeSaysWhatTheMoveWillDo(t *testing.T) {
	e := &Engine{}
	if got := e.chanSwitchAbility("mt798x-wmac"); !got.Announces || got.Why != "" {
		t.Fatalf("an uncaught driver reads as seamless, got %+v", got)
	}
	// Unknown reads as seamless ON PURPOSE: the move WILL be attempted by
	// announcement, and falls back by itself if it cannot be. Claiming
	// "drops clients" until something has been tried would describe hardware
	// this box has never met.
	if got := e.chanSwitchAbility("mt7921u"); got.Announces || got.Why == "" {
		t.Fatalf("a known refuser must say so and say why, got %+v", got)
	}
}

// The announcement's own command is unchanged and still tested where it lives
// (radioctl_test.go); what is new is that a mode outside the two is refused
// before any hardware is touched, rather than reaching hostapd as a typo.
func TestAModeOutsideTheTwoIsRefused(t *testing.T) {
	e := &Engine{cfg: Config{Demo: true}}
	if _, err := e.MoveChannel("wlan0", 40, 20, "seamless"); err == nil {
		t.Fatal("a mode nobody implements must be refused, not guessed at")
	} else if !strings.Contains(err.Error(), MethodAnnounce) {
		t.Fatalf("the refusal must name what IS accepted, got %q", err)
	}
}
