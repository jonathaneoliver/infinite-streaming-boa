package boa

import (
	"encoding/hex"
	"fmt"
	"strings"
)

/*
 * Reading the management frames themselves, rather than hostapd's summary.
 *
 * hostapdmonitor.go reads hostapd's COOKED events: BSS-TM-RESP, AP-STA-*,
 * BEACON-RESP-RX. Those are summaries, and a summary discards. The worked
 * example is a transition refusal: the client answers a steer with
 *
 *	status_code=6  "Reject -- STA BSS Transition Candidate List Provided"
 *
 * and the frame carries the candidate list it is talking about. hostapd's event
 * reports the status code and drops the list, so the interface could say a
 * client refused but never where it asked to go instead -- which is the whole
 * of the question. MEASURED 2026-09-07: a client refusing a move to wlan-usb2
 * named wlan0, channel 6, at preference 255. It was not declining to move. It
 * was asking for the other band.
 *
 * With notify_mgmt_frames=1 hostapd forwards every management frame it RECEIVES
 * as a hexdump on the same control connection:
 *
 *	<3>AP-MGMT-FRAME-RECEIVED buf=<hex>
 *
 * RECEIVES is the load-bearing word. Frames the access point TRANSMITS never
 * appear here, so this cannot witness whether the box actually radiated a
 * deauth -- that needs a monitor-mode radio, and the onboard chip has none. See
 * docs/DATA-CONTRACT.md, source N.
 *
 * Everything below is pure: bytes in, a struct out, no kernel and no sockets.
 * That is deliberate. Frame parsing goes wrong on lengths, and a parser that
 * needs a radio and an associated client to exercise gets tested once by hand
 * and never again. All of it is driven from captured hex in mgmtframe_test.go.
 */

// 802.11 management frame subtypes, from the Frame Control field's first octet.
// Only the ones this box does something with are named; the rest are reported
// by number rather than dropped, because a frame arriving that we did not
// expect is itself worth seeing in verbose mode.
const (
	subtypeAssocReq   = 0
	subtypeReassocReq = 2
	subtypeDisassoc   = 10
	subtypeAuth       = 11
	subtypeDeauth     = 12
	subtypeAction     = 13
)

// Element IDs used below. IEEE 802.11 Table 9-92.
const (
	elemRMEnabled   = 70  // RM Enabled Capabilities -- what 802.11k it will do
	elemNeighborRep = 52  // Neighbor Report -- one candidate BSS
	elemExtendedCap = 127 // Extended Capabilities -- carries the BTM bit
)

// Action frame category and action, IEEE 802.11 Table 9-51.
const (
	categoryWNM       = 10
	actionBTMResponse = 8
)

// mgmtFrameHeaderLen is the fixed part every management frame starts with:
// frame control, duration, three addresses and the sequence control field.
const mgmtFrameHeaderLen = 24

// mgmtFrame is one received management frame, decoded as far as this box cares.
//
// Fields that do not apply to the subtype are left zero rather than made into a
// union: the alternative is a type switch at every use site to read one integer,
// and the zero value is unambiguous for all of them (reason 0 is reserved,
// there is no dialog token 0 in flight, an empty candidate list is empty).
type mgmtFrame struct {
	Subtype int
	// Src is the transmitter -- address 2. For everything here that is the
	// client, because these are frames the access point received.
	Src string
	// BSSID is address 3, which says WHICH of our radios received it. The
	// control socket already tells us that, but a frame that disagrees with the
	// socket it arrived on is worth being able to notice.
	BSSID string

	// Reason is set for deauthentication and disassociation.
	Reason int
	// CurrentAP is the BSS a reassociating client says it is coming FROM. It is
	// the only place a roam names its own origin; the station table can say
	// where a client is now and where it was last seen, but not what the client
	// itself believes it just left.
	CurrentAP string
	// RMCapabilities is the first octet of the RM Enabled Capabilities element,
	// with Present recording whether the element was there at all. Absent and
	// zero are different findings: absent means the client claims no 802.11k,
	// zero would mean it claims the element and no capabilities in it.
	RMCapabilities    byte
	RMCapabilitiesSet bool
	// BTMSupported is the Extended Capabilities BSS Transition bit. A client
	// that never set it and then ignores a steer has not refused anything.
	BTMSupported bool

	// DialogToken, Status and Candidates are the BSS Transition Management
	// Response: which request this answers, what it decided, and where it would
	// rather go.
	DialogToken int
	Status      int
	Candidates  []neighborCandidate
	// IsBTMResponse separates "a BTM response saying status 0" from "not a BTM
	// response at all", both of which leave Status zero.
	IsBTMResponse bool
}

// neighborCandidate is one entry from a Neighbor Report element: a BSS the
// client is naming, and how much it wants it.
type neighborCandidate struct {
	BSSID   string
	OpClass int
	Channel int
	// Preference is the Candidate Preference subelement, 1..255, higher being
	// more wanted. 0 means "do not use". Absent is reported as -1 rather than
	// 0, because a client that names a candidate without stating a preference
	// is not telling us to avoid it.
	Preference int
}

// parseMgmtFrame decodes one AP-MGMT-FRAME-RECEIVED hexdump.
//
// Every read is bounds-checked against the remaining slice rather than against
// a length computed up front. Management frames arrive from client firmware of
// wildly varying quality, an element that lies about its own length is a normal
// thing to receive, and a panic here would take down the monitor goroutine for
// a radio and leave it silent for the rest of the daemon's life.
func parseMgmtFrame(dump string) (mgmtFrame, error) {
	raw, err := hex.DecodeString(strings.TrimSpace(dump))
	if err != nil {
		return mgmtFrame{}, fmt.Errorf("not a hexdump: %w", err)
	}
	if len(raw) < mgmtFrameHeaderLen {
		return mgmtFrame{}, fmt.Errorf("frame is %d bytes, shorter than an 802.11 header", len(raw))
	}
	// Type must be management (bits 2-3 of the first octet clear). hostapd
	// should not be sending anything else through this event, so a frame that
	// is not management means we are misreading the stream rather than looking
	// at an unusual client.
	if raw[0]&0x0c != 0 {
		return mgmtFrame{}, fmt.Errorf("frame type is not management (fc=0x%02x)", raw[0])
	}

	f := mgmtFrame{
		Subtype: int(raw[0] >> 4),
		Src:     macString(raw[10:16]),
		BSSID:   macString(raw[16:22]),
	}
	body := raw[mgmtFrameHeaderLen:]

	switch f.Subtype {
	case subtypeDeauth, subtypeDisassoc:
		// Reason code, little-endian, and nothing else that matters here.
		if len(body) >= 2 {
			f.Reason = int(body[0]) | int(body[1])<<8
		}
	case subtypeAssocReq:
		// Capability info and listen interval, then the elements.
		if len(body) >= 4 {
			f.readElements(body[4:])
		}
	case subtypeReassocReq:
		// As association, plus the current AP address in between.
		if len(body) >= 10 {
			f.CurrentAP = macString(body[4:10])
			f.readElements(body[10:])
		}
	case subtypeAction:
		f.readAction(body)
	}
	return f, nil
}

// readAction decodes the action frames this box acts on, which is currently one.
func (f *mgmtFrame) readAction(body []byte) {
	if len(body) < 2 || body[0] != categoryWNM || body[1] != actionBTMResponse {
		return
	}
	// Dialog token, status code, BSS termination delay.
	if len(body) < 5 {
		return
	}
	f.IsBTMResponse = true
	f.DialogToken = int(body[2])
	f.Status = int(body[3])
	rest := body[5:]

	// The Target BSSID field is optional and its presence is not stated by any
	// length field -- 802.11 says it is included when the status is Accept, and
	// implementations differ about the rest. Rather than trust the status code,
	// look: a Neighbor Report element starts with its own ID, so if the next
	// byte is that ID the candidate list starts here and there is no target.
	//
	// MEASURED 2026-09-07: a status-6 response from a real client went straight
	// to element 52 with no target BSSID, which is what this branch is for.
	if len(rest) > 0 && rest[0] != elemNeighborRep && len(rest) >= 6 {
		rest = rest[6:]
	}
	f.readElements(rest)
}

// readElements walks an information-element list, taking only what is wanted.
//
// The walk stops at the first element that will not fit rather than skipping
// it: once a length is wrong the position in the stream is lost, and every
// element after it would be read from the wrong offset. Half an answer from a
// malformed frame beats a confident wrong one.
func (f *mgmtFrame) readElements(b []byte) {
	for len(b) >= 2 {
		id, n := int(b[0]), int(b[1])
		if len(b) < 2+n {
			return
		}
		val := b[2 : 2+n]
		switch id {
		case elemRMEnabled:
			if n >= 1 {
				f.RMCapabilities, f.RMCapabilitiesSet = val[0], true
			}
		case elemExtendedCap:
			// BSS Transition is bit 19, i.e. bit 3 of the fourth octet. A
			// shorter element simply does not claim it.
			if n >= 3 {
				f.BTMSupported = val[2]&0x08 != 0
			}
		case elemNeighborRep:
			if c, ok := parseNeighborReport(val); ok {
				f.Candidates = append(f.Candidates, c)
			}
		}
		b = b[2+n:]
	}
}

// parseNeighborReport decodes one Neighbor Report element body.
//
// Fixed part is BSSID, BSSID Information, operating class, channel and PHY
// type -- 13 bytes -- followed by subelements, of which only the candidate
// preference is read.
func parseNeighborReport(v []byte) (neighborCandidate, bool) {
	const fixed = 13
	if len(v) < fixed {
		return neighborCandidate{}, false
	}
	c := neighborCandidate{
		BSSID:      macString(v[0:6]),
		OpClass:    int(v[10]),
		Channel:    int(v[11]),
		Preference: -1,
	}
	sub := v[fixed:]
	for len(sub) >= 2 {
		id, n := int(sub[0]), int(sub[1])
		if len(sub) < 2+n {
			break
		}
		// Subelement 3 is Candidate Preference, one octet.
		if id == 3 && n >= 1 {
			c.Preference = int(sub[2])
		}
		sub = sub[2+n:]
	}
	return c, true
}

// macString renders six bytes as a lower-case colon-separated address, matching
// normMAC so these can be compared with the addresses everything else uses.
func macString(b []byte) string {
	if len(b) < 6 {
		return ""
	}
	return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x", b[0], b[1], b[2], b[3], b[4], b[5])
}

// rmCapabilitySummary renders the RM Enabled Capabilities octet in words.
//
// The reason this is worth spelling out: "no beacon report came back" and "this
// client never claimed it could produce one" look identical from the outside,
// and only one of them is a fault. MEASURED 2026-09-07, both clients on this
// box advertise Beacon Table and neither advertises passive or active beacon
// measurement -- so hostapd refuses those requests before they are ever sent,
// and the silence is correct behaviour rather than a bug. See #228.
func rmCapabilitySummary(v byte) string {
	var have []string
	for _, c := range []struct {
		bit  byte
		name string
	}{
		{0x01, "link measurement"},
		{0x02, "neighbor report"},
		{0x10, "beacon passive"},
		{0x20, "beacon active"},
		{0x40, "beacon table"},
	} {
		if v&c.bit != 0 {
			have = append(have, c.name)
		}
	}
	if len(have) == 0 {
		return "none"
	}
	return strings.Join(have, ", ")
}

// disconnectReason renders an 802.11 reason code, IEEE 802.11 Table 9-49.
//
// Only the codes a client actually sends to an access point are named. The
// distinction worth having is why a device LEFT: one that roamed away, one that
// timed out and one that was pushed all vanish from the station table in
// exactly the same way, and the reason code is the only thing that tells them
// apart.
func disconnectReason(code int) string {
	switch code {
	case 1:
		return "unspecified"
	case 2:
		return "its authentication is no longer valid"
	case 3:
		return "it is leaving or has left"
	case 4:
		return "inactivity"
	case 5:
		return "the access point is out of capacity"
	case 6:
		return "it received a data frame while not associated"
	case 7:
		return "it received a data frame while not associated"
	case 8:
		return "it is leaving or has left"
	case 9:
		return "it was not authenticated"
	case 34:
		return "poor link quality"
	default:
		return fmt.Sprintf("reason code %d", code)
	}
}

// withArticle prefixes "a" or "an" as the following word requires.
//
// Small, and worth having rather than hardcoding "a" at the call site: the
// subtype names include "authentication" and "action frame", which produced
// "sent a authentication" in the activity log on the first run of this. A log
// that reads like it was assembled by string concatenation is one people stop
// reading carefully.
func withArticle(s string) string {
	if s == "" {
		return s
	}
	if strings.ContainsRune("aeiou", rune(s[0])) {
		return "an " + s
	}
	return "a " + s
}

// subtypeName renders a management subtype for a verbose log line.
func subtypeName(subtype int) string {
	switch subtype {
	case subtypeAssocReq:
		return "association request"
	case subtypeReassocReq:
		return "reassociation request"
	case subtypeDisassoc:
		return "disassociation"
	case subtypeAuth:
		return "authentication"
	case subtypeDeauth:
		return "deauthentication"
	case subtypeAction:
		return "action frame"
	default:
		return fmt.Sprintf("management subtype %d", subtype)
	}
}
