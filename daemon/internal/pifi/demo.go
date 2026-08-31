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
	// ladder makes this client behave as an ABR player rather than as a
	// generic bulk transfer: it delivers the highest rung that fits inside its
	// cap, and rebuffers against the cap when none does.
	//
	// Without one, a ladder sweep in demo mode has nothing to discover -- the
	// generic model just tracks the cap, so every level reads as saturated and
	// the whole feature is undevelopable without hardware.
	ladder []float64
}

// demoSegmentSec is the synthetic content's segment duration. It sets the
// cadence of the fetch bursts, and therefore how many segments an observation
// window has to span before its mean means anything.
const demoSegmentSec = 6.0

// abrThroughput is what the wire actually carries: bursts, not a steady rate.
//
// A player does not trickle its rendition out at the encoding bitrate. It
// fetches a segment as fast as the link allows and then goes idle until the
// next one, so the 1 Hz series is bimodal -- line rate, then zero. Over a whole
// segment period the MEAN is the rendition; no individual sample is.
//
// Modelling this matters. The first version of this fleet emitted a continuous
// rendition rate, which is a shape real traffic never has, and it let a sweep
// that took the MEDIAN of its window pass every test here and then read 16.75
// Mbps off a real iPhone that was delivering 13.52.
func (d *demoClient) abrThroughput(capMbps, t float64) float64 {
	rendition := d.abrRate(capMbps)

	// During a fetch the link delivers whatever it can, capped if there is one.
	burst := d.baseMbps
	if capMbps > 0 && capMbps < burst {
		burst = capMbps
	}
	// A rendition needing the whole link leaves no idle time: the fetch never
	// finishes early, so throughput is continuous rather than bursty. This is
	// also what a rebuffering player looks like.
	duty := rendition / burst
	if duty >= 0.95 {
		return burst * (0.93 + 0.05*rand.Float64())
	}
	// The fetch occupies the first `duty` of each segment period, idle for the
	// rest. Fractional rather than a binary on/off, because a 1 Hz sample is an
	// average over its second: a fetch ending mid-second gives a partial value,
	// and pretending otherwise quantises the duty cycle to whole seconds.
	return burst * onFraction(t+d.phase, demoSegmentSec, duty*demoSegmentSec) *
		(0.9 + 0.2*rand.Float64())
}

// onFraction is how much of the one-second sample beginning at t falls inside
// the fetch phase of a segment period: the fetch occupies the first onSec of
// every periodSec.
//
// Fractional, NOT a binary on/off. A 1 Hz throughput sample is an AVERAGE over
// its second, so a fetch that ends mid-second yields a partial value. Modelling
// it as all-or-nothing quantises the duty cycle to whole seconds -- a 0.56 duty
// over a 6 s period realises as 4/6 = 0.67 -- which shows up as a rate-dependent
// over-read that looks exactly like a detector bias but is not one.
func onFraction(t, periodSec, onSec float64) float64 {
	start := math.Mod(t, periodSec)
	end := start + 1
	covered := math.Max(0, math.Min(math.Min(end, periodSec), onSec)-math.Min(start, onSec))
	if end > periodSec { // the sample straddles into the next period
		covered += math.Max(0, math.Min(end-periodSec, onSec))
	}
	return covered
}

// abrRate is the demo player's rendition choice: the highest rung that fits
// inside the cap with a little headroom. Below the bottom rung it rebuffers,
// which consumes the cap rather than going quiet.
func (d *demoClient) abrRate(capMbps float64) float64 {
	top := d.ladder[len(d.ladder)-1]
	if capMbps <= 0 {
		return top
	}
	best := -1.0
	for _, r := range d.ladder {
		if r <= capMbps*0.95 {
			best = r
		}
	}
	if best < 0 {
		return capMbps * 0.95
	}
	return best
}

func newDemoFleet() []*demoClient {
	return []*demoClient{
		{mac: "a4:83:e7:2c:19:04", label: "iPhone 15", medium: "wifi", port: "wlan0",
			ip: "192.168.1.42", present: true, phase: 0, baseMbps: 45, signal: -52},
		// Two ABR players with deliberately different ladders, because the
		// point of keying a ladder by service is that no two are alike.
		{mac: "dc:a6:32:6b:80:11", label: "Apple TV", medium: "wifi", port: "wlan0",
			ip: "192.168.1.51", present: true, phase: 2.1, baseMbps: 78, signal: -61,
			ladder: []float64{0.4, 0.9, 1.8, 3.2, 5.8, 9.5, 15}},
		{mac: "00:1a:2b:3c:4d:5e", label: "Test rig (wired)", medium: "wired", port: "lan0",
			ip: "192.168.1.77", present: true, phase: 4.2, baseMbps: 94, signal: 0, wired: true,
			ladder: []float64{0.3, 0.75, 1.5, 3, 6, 12}},
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

// demoOffer is the rate a synthetic device would like to send at time t.
//
// A slow swell rather than a flat line, so the sparkline and the axis modes can
// actually be judged. Split out from demoTick because the backfill below has to
// evaluate the same curve at past instants -- two copies of it would drift, and
// the seam where a replayed hour met the live tick would look like a bug in the
// chart rather than a difference in the fixture.
func demoOffer(d *demoClient, t float64) float64 {
	return d.baseMbps * (0.55 + 0.45*math.Sin(t/11+d.phase))
}

// demoBackfill replays an hour of synthetic history into the ring at start-up.
//
// Without it, demo mode cannot exercise the thing it exists to exercise: the
// 15m and 1h ranges, the server-side decimation, and the reload seed are all
// invisible until the daemon has been running for an hour. This makes them
// visible on the first page load.
//
// Demo mode only. Inventing history on a real box would put traffic on the
// chart that never happened, which is precisely the lie this interface is
// built not to tell.
func (e *Engine) demoBackfill() {
	now := time.Now()
	for _, d := range e.demo {
		if !d.present || d.ip == "" {
			continue
		}
		for age := historyLen; age >= 1; age-- {
			at := now.Add(-time.Duration(age) * time.Second)
			// The same jitter the live tick applies. Without it the replayed
			// hour is a clean sine and the live tail is noisy, so the seam
			// between them reads as a rendering bug in the chart rather than
			// the boundary of a fixture.
			offer := demoOffer(d, float64(at.UnixMilli())/1000.0) * (0.85 + 0.3*rand.Float64())
			e.hist.Add(d.mac, Sample{
				T: at.UnixMilli(), Down: offer, Up: offer * 0.12,
			})
		}
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
			offer := demoOffer(d, t) * (0.85 + 0.3*rand.Float64())

			// A running sweep drives the cap itself, exactly as it does in
			// production, so the dev loop exercises the real code path rather
			// than a demo-only imitation of it.
			downShape := pol.Down
			upShape := pol.Up
			if !pol.Enabled {
				downShape, upShape = Shape{}, Shape{}
			}
			if sh, ok := e.sweep.Override(d.mac); ok {
				downShape = sh
			}
			// A pattern drives the synthetic client too, so the dev loop shows
			// a chart that actually responds to the timeline being authored.
			// Without this the editor would look finished while proving
			// nothing, which is the failure mode a demo mode exists to avoid.
			if ds, us, ok := e.player.Override(d.mac); ok {
				downShape, upShape = ds, us
			}

			down := offer
			if len(d.ladder) > 0 {
				// An ABR player delivers a rendition, not "whatever fits" --
				// but it delivers it in bursts with idle gaps between, which is
				// the shape the sweep's statistic has to survive.
				down = d.abrThroughput(downShape.RateMbps, t)
			} else if downShape.RateMbps > 0 {
				// Sit just under the cap the way a real TCP flow does, rather
				// than pinning exactly to it.
				down = math.Min(offer, downShape.RateMbps*(0.93+0.05*rand.Float64()))
			}
			up := offer * 0.12
			if upShape.RateMbps > 0 {
				up = math.Min(up, upShape.RateMbps*(0.93+0.05*rand.Float64()))
			}

			c.DownCounters = e.demoCounters("d/"+d.mac, down, downShape, now)
			c.UpCounters = e.demoCounters("u/"+d.mac, up, upShape, now)

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

	// Recorded before publishing, exactly as the real tick does, so the demo
	// exercises the same history path the interface reads back -- and so the
	// sweep's detector has real telemetry to work from here too.
	for i := range clients {
		e.hist.Add(clients[i].MAC, Sample{
			T:    now.UnixMilli(),
			Down: clients[i].DownCounters.ThroughputMbps,
			Up:   clients[i].UpCounters.ThroughputMbps,
			Cap:  clients[i].DownCounters.CapMbps,
		})
	}
	live := make(map[string]bool, len(clients))
	for _, c := range clients {
		live[c.MAC] = c.Present && c.Shapeable
	}
	e.sweep.Advance(now, sweepObserver{hist: e.hist, live: live})
	e.storeSweepResult()
	e.player.Advance(now)
	// Read the view AFTER advancing, as production does, or demo would show a
	// sweep one tick behind the one it is actually running and the two would
	// disagree about when a level changed.
	for i := range clients {
		clients[i].Sweep = e.sweep.View(clients[i].MAC)
		clients[i].PatternRun = e.player.View(clients[i].MAC)
	}

	e.mu.Lock()
	e.rev++
	snap := Snapshot{
		Revision: e.rev, ControlRevision: e.ctrlRev, Time: now.UnixMilli(),
		Clients: clients,
		Caps: Capabilities{
			Shaping: true, Uplink: true, Radio: true,
			WlanIface: "wlan0", UplinkIf: "eth0",
			// On so the deep links and the measurement note are visible
			// during UI development; neither is actually running here.
			Ntopng: true, NtopngPort: ntopngPort,
			Iperf: true, IperfPort: iperfPort,
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
