package pifi

import (
	"math"
	"math/rand"
	"time"
)

// Demo mode serves the real API with synthesised clients.
//
// It exists so the web interface can be developed on a laptop with no Pi, no
// root, and no kernel involvement -- but through the SAME types, the same JSON
// encoding and the same SSE transport as production. A hand-written mock server
// would drift from the daemon the first time a field changed; this cannot,
// because it is the daemon.
//
// The synthetic fleet deliberately includes the awkward states, since those are
// the ones that are laborious to reproduce on real hardware and therefore the
// ones that never get styled: a client that is associated but has not taken an
// address, and a client that is configured but currently absent.

type demoClient struct {
	mac, label, medium, port string
	ip                       string
	present                  bool
	// phase offsets the traffic waveform so the devices do not move in unison.
	phase float64
	// baseMbps is what this device would pull on an unconditioned link.
	baseMbps float64
	signal   float64
	wired    bool
}

func newDemoFleet() []*demoClient {
	return []*demoClient{
		{mac: "a4:83:e7:2c:19:04", label: "iPhone 15", medium: "wifi", port: "wlan0",
			ip: "192.168.1.42", present: true, phase: 0, baseMbps: 45, signal: -52},
		{mac: "dc:a6:32:6b:80:11", label: "Apple TV", medium: "wifi", port: "wlan0",
			ip: "192.168.1.51", present: true, phase: 2.1, baseMbps: 78, signal: -61},
		{mac: "00:1a:2b:3c:4d:5e", label: "Test rig (wired)", medium: "wired", port: "lan0",
			ip: "192.168.1.77", present: true, phase: 4.2, baseMbps: 94, signal: 0, wired: true},
		{mac: "f0:18:98:1d:aa:7c", label: "Pixel 8", medium: "wifi", port: "wlan0",
			ip: "192.168.1.63", present: true, phase: 1.1, baseMbps: 22, signal: -74},
		// Associated but has not completed DHCP: real, common, and the state
		// that most often renders badly because nobody sees it while building.
		{mac: "b8:27:eb:44:31:9a", label: "", medium: "wifi", port: "wlan0",
			ip: "", present: true, phase: 3.3, baseMbps: 0, signal: -68},
		// Configured earlier, not currently on the network.
		{mac: "3c:22:fb:81:52:d0", label: "Roku (bedroom)", medium: "", port: "",
			ip: "", present: false, phase: 0, baseMbps: 0, signal: 0},
	}
}

// demoTick synthesises one frame. Throughput respects whatever cap the operator
// has set, so dragging a slider visibly changes the graph -- without that the
// controls feel dead and the interface cannot really be judged.
func (e *Engine) demoTick() {
	now := time.Now()
	t := float64(now.UnixMilli()) / 1000.0
	policies := e.st.All()

	clients := make([]Client, 0, len(e.demo))
	for _, d := range e.demo {
		pol, ok := policies[d.mac]
		if !ok {
			pol = Policy{MAC: d.mac, Enabled: true}
		}
		label := pol.Label
		if label == "" {
			label = d.label
		}
		if label == "" {
			label = d.mac
		}

		var v6 []string
		if d.ip != "" && d.medium == "wifi" {
			v6 = []string{"fdd5:a04f:f953:4412::" + d.mac[len(d.mac)-2:]}
		}
		c := Client{
			MAC: d.mac, IP: d.ip, IPv6: v6, Label: label, Medium: d.medium, Port: d.port,
			Present: d.present, Shapeable: d.ip != "" && d.present,
			Policy: pol, LastSeen: now.UnixMilli(),
			RTTAddedMs:  pol.Down.DelayMs + pol.Up.DelayMs,
			SubCounters: map[string]Counters{},
		}

		if !d.wired && d.present {
			// Signal wanders slowly rather than jumping, so the UI's colour
			// bands can actually be evaluated.
			d.signal += (rand.Float64() - 0.5) * 1.5
			d.signal = math.Max(-85, math.Min(-40, d.signal))
			c.Station = &Station{
				MAC: d.mac, SignalDBm: int(d.signal),
				TxPhyMbps:    300 + 200*math.Sin(t/17+d.phase),
				RxPhyMbps:    280 + 180*math.Sin(t/19+d.phase),
				ConnectedSec: int(t) % 8000,
				TxFailed:     uint64(t/7) % 9000,
			}
		}

		if d.present && d.ip != "" {
			// A slow swell plus jitter reads as real traffic rather than a
			// synthetic sine, which matters when judging the sparkline.
			wave := 0.55 + 0.45*math.Sin(t/11+d.phase)
			noise := 0.85 + 0.3*rand.Float64()
			offer := d.baseMbps * wave * noise

			down := offer
			if pol.Enabled && pol.Down.RateMbps > 0 {
				// Sit just under the cap the way a real TCP flow does, rather
				// than pinning exactly to it.
				down = math.Min(offer, pol.Down.RateMbps*(0.93+0.05*rand.Float64()))
			}
			up := offer * 0.12
			if pol.Enabled && pol.Up.RateMbps > 0 {
				up = math.Min(up, pol.Up.RateMbps*(0.93+0.05*rand.Float64()))
			}

			c.DownCounters = e.demoCounters("d/"+d.mac, down, pol.Down, now)
			c.UpCounters = e.demoCounters("u/"+d.mac, up, pol.Up, now)

			for _, sub := range pol.Sub {
				if !sub.Enabled {
					continue
				}
				share := down * 0.4
				if sub.Down.RateMbps > 0 {
					share = math.Min(share, sub.Down.RateMbps*0.95)
				}
				c.SubCounters[sub.ID] = e.demoCounters("s/"+sub.ID, share, sub.Down, now)
			}
		}
		clients = append(clients, c)
	}

	e.mu.Lock()
	e.rev++
	snap := Snapshot{
		Revision: e.rev, ControlRevision: e.ctrlRev, Time: now.UnixMilli(),
		Clients: clients,
		Caps: Capabilities{
			Shaping: true, Uplink: true, Radio: true,
			WlanIface: "wlan0", UplinkIf: "eth0",
			// On so the deep links are visible during UI development.
			Ntopng: true, NtopngPort: ntopngPort,
		},
		Notices: []Notice{
			{"error", "DEMO MODE - these clients are synthetic. No traffic is being conditioned."},
			{"info", "Wi-Fi airtime is shared. Conditioning is additive on top of a variable " +
				"radio baseline, so one client's traffic still affects another's " +
				"achievable rate no matter what these limits say."},
		},
	}
	e.snap = snap
	subs := make([]chan Snapshot, 0, len(e.subs))
	for ch := range e.subs {
		subs = append(subs, ch)
	}
	e.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- snap:
		default:
		}
	}
}

// demoCounters accumulates byte totals from a rate, so the counter columns and
// the throughput derivation behave the way they do in production.
func (e *Engine) demoCounters(key string, mbps float64, sh Shape, now time.Time) Counters {
	prev := e.demoBytes[key]
	prev += uint64(mbps * 1e6 / 8 * e.cfg.Tick.Seconds())
	e.demoBytes[key] = prev

	c := Counters{
		Bytes: prev, Packets: prev / 1400,
		ThroughputMbps: mbps,
		CapMbps:        sh.RateMbps,
	}
	if sh.RateMbps > 0 {
		// A throttled class hits its ceiling constantly; surfacing that here
		// keeps the "overlimits is not an error" note honest in demo too.
		c.Overlimits = prev / 20000
		c.Backlog = uint64(sh.RateMbps * 1e6 / 8 * (sh.DelayMs + 20) / 1000)
	}
	if sh.LossPct > 0 {
		c.Drops = uint64(float64(c.Packets) * sh.LossPct / 100)
	}
	return c
}
