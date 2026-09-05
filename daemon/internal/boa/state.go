package boa

import (
	"fmt"
	"net"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"
)

// Config is the daemon's view of the box it is running on.
type Config struct {
	// Version is the running build's version string, threaded in from main so
	// the snapshot can report which build a box is on. See main.version.
	Version string
	Bridge  string // br-lan
	WANPort string // the port cabled to the existing network; uplink shaped here
	// WlanPorts is every radio serving the access point, in preference order.
	//
	// A list rather than one name because the box can run two radios at once --
	// the onboard chip on 2.4GHz and a USB adapter on 5GHz, the way a
	// dual-band router does. It used to be a single interface, and everything
	// associated to any other radio was invisible: not in the station table the
	// daemon read, not conditioned, and absent from the device list while
	// happily passing traffic.
	//
	// Empty is not a valid state; NewEngine falls back to one default name.
	WlanPorts []string
	LanPort   string // lan0, the USB adapter (may be absent)
	StatePath string
	// Addr is where the interface is served, e.g. ":80". The shaper needs it:
	// the port the interface answers on is the one port whose traffic must
	// never be conditioned.
	Addr string
	Tick time.Duration
	// Demo serves synthetic clients and touches no kernel state, so the web
	// interface can be developed without a Pi. See demo.go.
	Demo bool
}

// PrimaryWlan is the radio reported wherever a single name is still wanted --
// the header's readout, and the older API fields that predate two radios.
func (c Config) PrimaryWlan() string {
	if len(c.WlanPorts) == 0 {
		return ""
	}
	return c.WlanPorts[0]
}

// IsWlan reports whether an interface is one of the radios this daemon watches.
// The distinction that matters: a wireless interface the daemon does NOT watch
// carries clients nobody is conditioning.
func (c Config) IsWlan(name string) bool {
	for _, p := range c.WlanPorts {
		if p == name {
			return true
		}
	}
	return false
}

// counterSample remembers one class's byte count so throughput can be derived
// between polls. The kernel exposes totals, never rates.
type counterSample struct {
	bytes uint64
	at    time.Time
}

// Engine owns all mutable state and is the only thing that talks to the Shaper.
//
// Revisions follow the pattern proven in the streaming test harness:
//
//	Revision        bumps every tick, because telemetry always moves
//	ControlRevision bumps only when operator intent changed
//
// The UI resyncs slider positions on ControlRevision alone, so a telemetry
// update arriving mid-drag cannot yank a control out from under the cursor.
type Engine struct {
	mu    sync.RWMutex
	cfg   Config
	sh    *Shaper
	st    *Store
	pat   *PatternStore
	lad   *LadderStore
	chp   *ChannelStore
	learn *Learner

	// restore keeps the channel restore terminating: what is mid-move, how
	// many times each radio has been put back, and which have been given up
	// on. See channelrestore.go -- a restore that repeated would take the
	// access point down every tick.
	restore restoreState

	// radioLocks serialises reconfiguration per radio, so a move from the API
	// and a move from the tick's restore cannot interleave on one interface.
	radioMu    sync.Mutex
	radioLocks map[string]*sync.Mutex

	// airtime holds the last survey read per radio and the busy fraction
	// derived from the delta. Sampled on its own slow timer, never on the tick:
	// see airtime.go for why fifteen seconds is the floor.
	airtime airtimeWatch

	// pendingSteers is every 802.11v transition request still waiting for the
	// client's answer, keyed by MAC. The answer arrives asynchronously on the
	// monitor connection, so it has to be matched against something; and a
	// request that is never answered is itself the result, which is why these
	// are aged out loudly rather than dropped. See hostapdmonitor.go.
	pendingSteers map[string]pendingSteer

	rev, ctrlRev uint64
	snap         Snapshot

	// prev is keyed "dev/minor" and holds the last byte count seen there.
	prev map[string]counterSample

	// lastAssoc is when each MAC was last present in the radio's station
	// table. Wi-Fi association is the one fact about a wireless client that is
	// not a guess -- the driver either has the station or it does not -- so it
	// is what decides whether a wireless client is still here. In memory only,
	// like lastActive: it rebuilds on the first tick after a restart.
	lastAssoc map[string]int64

	// stationRadio maps a MAC to the radio it is associated to, refreshed each
	// tick. With two radios serving, a per-client link event must go to the
	// control socket of the radio holding that station; the other one answers
	// with a station it has never heard of.
	stationRadio map[string]string
	// assocSeen holds the real time hostapd reported a client's most recent
	// association change, so the tick can stamp its own event with it rather
	// than with the moment it noticed. See noteAssoc.
	assocSeen map[string]assocObs

	// events is the in-memory log of what CHANGED -- joins, roams, radio
	// moves, actions. Everything else here is state; see events.go for why
	// that is not enough. prevRadio is the previous tick's associations, which
	// is the only way to notice a client moved between radios.
	events         eventLog
	prevRadio      map[string]string
	prevRadioState map[string]string
	// prevAPServing is the previous tick's per-radio serving state. Separate
	// from prevRadioState because a radio's channel and whether it is actually
	// serving are independent facts, and the second one is invisible in the
	// first -- see noteAPServing.
	prevAPServing map[string]string

	// radioOn caches each radio's channel/width/mode for the device list,
	// refreshed on a slow timer: it changes only when something deliberately
	// changes it, and asking hostapd per tick is a round-trip per radio for a
	// value that is almost always unchanged.
	radioOn map[string]*RadioOn
	// radioOnAt is PER INTERFACE. It was a single timestamp shared by every
	// radio, which quietly disabled the cache for all but one of them: the
	// first radio refreshed and stamped "now", so the second still looked fresh
	// and returned its stale entry -- and did so again on the next tick, and
	// for ever. One radio's channel, width and serving state could therefore
	// never change again as far as the tick was concerned. Invisible with one
	// radio; guaranteed with two.
	radioOnAt map[string]time.Time

	// scanSeen is the last band scan per radio, kept so the interface can
	// colour its channel controls from a measurement rather than a guess. In
	// memory like the event log, and for the same reason: it describes a
	// moment, and a stale one restored from disk would be worse than none.
	scanSeen map[string]ScanSummary

	// lastActive is when each MAC was last moving more than a trickle.
	// Telemetry, so it is held in memory and never written to the store: it
	// rebuilds itself within seconds of a restart for anything actually doing
	// something, which is the only case it is consulted for.
	lastActive map[string]int64

	demo      []*demoClient
	demoBytes map[string]uint64

	// ntopng liveness, re-probed occasionally rather than every tick: a TCP
	// dial per second for a link that rarely changes state is pure waste.
	ntopUp      bool
	ntopChecked time.Time

	namesPath string

	// hist is the per-client throughput series a reloading browser seeds from.
	hist     *History
	histPath string

	// sweep discovers rendition ladders by stepping a device's cap down. It
	// holds its own lock and never reaches back into the engine, so calling it
	// from the tick is safe in either order.
	sweep *Sweeper

	// player drives devices along authored patterns. Same contract as sweep --
	// own lock, no reach back into the engine, stepped from the tick.
	player *Player

	subs map[chan Snapshot]struct{}
}

// sweepObserver is the engine's answer to what a sweep needs to see: the recent
// throughput series, and which devices are still there to measure. Rebuilt each
// tick from that tick's client list, so presence is never stale.
type sweepObserver struct {
	hist *History
	live map[string]bool
}

func (o sweepObserver) Window(mac string, from, to time.Time) []Sample {
	return o.hist.Between(mac, from, to)
}
func (o sweepObserver) Live(mac string) bool { return o.live[mac] }

func NewEngine(cfg Config) *Engine {
	if cfg.Tick == 0 {
		cfg.Tick = time.Second
	}
	return &Engine{
		cfg:          cfg,
		sh:           NewShaper(cfg.WANPort, cfg.Bridge, managementPorts(cfg.Addr)),
		st:           NewStore(cfg.StatePath),
		pat:          NewPatternStore(patternsPathFor(cfg.StatePath)),
		lad:          NewLadderStore(ladderPathFor(cfg.StatePath)),
		chp:          NewChannelStore(channelsPathFor(cfg.StatePath)),
		learn:        NewLearner(cfg.Bridge, append(append([]string{}, cfg.WlanPorts...), cfg.LanPort)...),
		prev:         map[string]counterSample{},
		lastActive:   map[string]int64{},
		lastAssoc:    map[string]int64{},
		stationRadio: map[string]string{},
		demo:         newDemoFleet(),
		demoBytes:    map[string]uint64{},
		hist:         NewHistory(),
		sweep:        &Sweeper{},
		player:       &Player{},
		subs:         map[chan Snapshot]struct{}{},
	}
}

func patternsPathFor(statePath string) string {
	return filepath.Join(filepath.Dir(statePath), "patterns.json")
}

func ladderPathFor(statePath string) string {
	return filepath.Join(filepath.Dir(statePath), "ladder.json")
}

func channelsPathFor(statePath string) string {
	return filepath.Join(filepath.Dir(statePath), "channels.json")
}

func (e *Engine) Store() *Store { return e.st }

// PatternStore holds the box's saved patterns. Beside policy.json rather than
// inside it: policies persist as a bare object keyed by MAC, and folding
// patterns in would change that file's shape and need a migration on every
// existing box for no benefit over a second small file.
func (e *Engine) PatternStore() *PatternStore { return e.pat }

// ChannelStore holds the operator's chosen channel per radio. Beside
// policy.json rather than in hostapd's config, which has no single file a
// channel belongs in -- see ChannelStore.
func (e *Engine) ChannelStore() *ChannelStore { return e.chp }

// LadderStore holds THE ladder for the box. See LadderStore.
func (e *Engine) LadderStore() *LadderStore { return e.lad }
func (e *Engine) Shaper() *Shaper           { return e.sh }
func (e *Engine) Sweeper() *Sweeper         { return e.sweep }
func (e *Engine) Player() *Player           { return e.player }

// Config exposes the running configuration for handlers that must name a
// radio -- an adapter pattern's lanes are interfaces, and one this box does not
// have would fire against nothing.
func (e *Engine) Config() Config { return e.cfg }

// RadioServing reports whether a radio can currently carry clients, which is
// the question an adapter pattern's precondition asks: a bounce needs two.
func (e *Engine) RadioServing(iface string) bool { return e.radioReady(iface) == nil }

// Start brings up the kernel scaffolding and the passive listeners, then ticks
// forever. Shaping failure is reported through capabilities rather than being
// fatal: a box that serves a UI explaining why conditioning is unavailable is
// far more useful than one that exits and leaves a headless Pi silent.
func (e *Engine) Start() {
	if e.cfg.Demo {
		// No shaper, no packet socket: demo mode must run unprivileged on a
		// developer's laptop.
		//
		// An hour of synthetic history first, so the long ranges and the reload
		// seed can be judged on the first page load rather than an hour in.
		e.demoBackfill()
		go func() {
			t := time.NewTicker(e.cfg.Tick)
			defer t.Stop()
			for range t.C {
				e.demoTick()
			}
		}()
		e.demoTick()
		return
	}
	if err := e.sh.Setup(); err != nil {
		fmt.Printf("infinite-streaming-boa: shaping unavailable: %v\n", err)
	}
	// Clear any deadzone ban left in hostapd's deny list by a daemon that died
	// mid-outage, so a client is never stranded off the AP across a restart.
	e.clearDenyACL()
	e.restoreRadioPower()
	// A monitor connection per radio, for the messages hostapd sends unasked.
	// Everything else here talks to hostapd in request/reply, which cannot see
	// a client's answer to a steer -- see hostapdmonitor.go.
	e.watchHostapdEvents()
	// How busy each radio's channel is, sampled slowly. Its own goroutine
	// because the interval that makes it meaningful is far longer than a tick.
	go e.watchAirtime()
	// Devices announce only occasionally -- on join, on wake, when services
	// change -- so an in-memory-only name table means every daemon restart
	// drops every client back to a bare MAC until the next announcement,
	// which can be many minutes away. From outside that is indistinguishable
	// from the feature breaking.
	// tmpfs, not the state directory: history must survive a daemon restart --
	// which happens on every deploy -- but writing a time series to the SD card
	// every second is how a Pi appliance kills its storage.
	e.histPath = "/run/infinite-streaming-boa/history.json"
	e.hist.Load(e.histPath)

	e.namesPath = filepath.Join(filepath.Dir(e.cfg.StatePath), "names.json")
	namesPath := e.namesPath
	e.learn.LoadNames(namesPath)
	go func() {
		t := time.NewTicker(2 * time.Minute)
		defer t.Stop()
		for range t.C {
			e.learn.SaveNames(namesPath)
			e.hist.Save(e.histPath)
		}
	}()
	go func() {
		if err := e.learn.Run(); err != nil {
			fmt.Printf("infinite-streaming-boa: passive learner stopped: %v\n", err)
		}
	}()
	go func() {
		t := time.NewTicker(e.cfg.Tick)
		defer t.Stop()
		for range t.C {
			e.tick()
		}
	}()
}

// BumpControl records that operator intent changed, which is what makes the UI
// accept new control values.
func (e *Engine) BumpControl() {
	e.mu.Lock()
	e.ctrlRev++
	e.mu.Unlock()
	// Apply immediately rather than waiting up to a full tick, so the UI sees
	// its own write reflected without a visible lag.
	if e.cfg.Demo {
		e.demoTick()
		return
	}
	e.tick()
}

// desired flattens stored policy into the flat list the Shaper reconciles
// against, resolving each device's CURRENT address at the moment of writing.
// Resolution happens here, once per tick, which is what lets a policy survive
// its device changing address.
func (e *Engine) desired(clients []Client) []Desired {
	var want []Desired
	for _, c := range clients {
		if c.IP == "" && len(c.IPv6) == 0 {
			continue // not shapeable, and nothing to attach a filter to
		}
		// Sub-classes first: they are more specific, and the Shaper installs
		// them at a filter priority that wins over the device default.
		for _, sub := range c.Policy.Sub {
			if !c.Policy.Enabled || !sub.Enabled || sub.Match.IsEmpty() {
				continue
			}
			want = append(want, Desired{
				Key: c.MAC + "/" + sub.ID, IP: c.IP, IPv6: c.IPv6, Port: c.Port,
				Match: sub.Match, Down: sub.Down, Up: sub.Up, IsSub: true,
			})
		}
		// The device class is created even when nothing is being enforced.
		// Throughput is derived from tc class counters, so without a class an
		// actively streaming client reports 0.00 Mbps and the interface looks
		// broken -- measuring only what is being shaped is backwards. An HTB
		// class with no netem leaf is a pure counter: writeNetem drops the leaf
		// for a clean shape, so this costs one class and one filter per client
		// and changes nothing about the traffic.
		down, up := c.Policy.Down, c.Policy.Up
		if !c.Policy.Enabled {
			// Disabled means "do not condition", not "do not measure".
			down, up = Shape{}, Shape{}
		}
		// A running ladder sweep drives the downlink cap itself, overriding
		// stored policy for the duration. Applied here rather than written to
		// the Store, so nothing has to be unwound: the override vanishes with
		// the run, and a daemon that dies mid-sweep comes back conditioning the
		// device exactly as the operator left it.
		if s, ok := e.sweep.Override(c.MAC); ok {
			down = s
		}
		// A running pattern drives both directions along its timeline, on the
		// same terms: applied here, never written to the Store, gone the
		// moment the run is. A sweep and a pattern both want the cap, so the
		// API refuses to start one while the other is running rather than
		// leaving the winner to whichever line of this function came last.
		if d, u, ok := e.player.Override(c.MAC); ok {
			down, up = d, u
		}
		want = append(want, Desired{
			Key: c.MAC, IP: c.IP, IPv6: c.IPv6, Port: c.Port, Down: down, Up: up,
		})
	}
	return want
}

// rate turns two byte totals into Mbps, handling the counter reset that occurs
// when a class is recreated. Without the reset check a recreate produces a
// wildly negative delta that renders as a spike or a hole in the chart.
func (e *Engine) rate(key string, bytes uint64, now time.Time) float64 {
	p, ok := e.prev[key]
	e.prev[key] = counterSample{bytes: bytes, at: now}
	if !ok || bytes < p.bytes {
		return 0 // first sample, or a new counter epoch
	}
	dt := now.Sub(p.at).Seconds()
	if dt <= 0 {
		return 0
	}
	return float64(bytes-p.bytes) * 8 / dt / 1e6
}

const ntopngPort = 3000

// glancesPort is where the glances web UI listens; the unit that puts it there
// is written by section 8e of scripts/customize.sh.
const glancesPort = 61208

// sshPort is exempted from shaping alongside the interface: the way out of a
// cap set too low is often the shell, and it must not be caught by the thing it
// is there to undo.
const sshPort = 22

// managementPorts lists the source ports whose traffic FROM this box is never
// conditioned. Everything else the box sends is, deliberately -- see
// writeExemptions.
//
// The interface's own port is derived from the address it was told to serve on
// rather than assumed to be 80, because -addr is a flag and a box serving on
// 8080 would otherwise exempt a port nothing is listening on while shaping the
// one that is: the interface would go dark exactly when a low cap made it
// indispensable.
func managementPorts(addr string) []int {
	web := 80
	if _, p, err := net.SplitHostPort(addr); err == nil {
		if n, err := strconv.Atoi(p); err == nil && n > 0 && n < 65536 {
			web = n
		}
	}
	ports := []int{web}
	for _, p := range []int{sshPort, ntopngPort, glancesPort} {
		if p != web {
			ports = append(ports, p)
		}
	}
	sort.Ints(ports)
	return ports
}

// iperfPort is where overlay/etc/systemd/system/iperf3.service listens.
//
// The target address is deliberately NOT computed here. The bridge holds
// several -- a DHCP address and a fixed rescue address on a private /24 that
// clients cannot reach -- and picking between them would be guessing at which
// one the reader can get to. The interface knows for certain: it is being
// served over one of them.
const iperfPort = 5201

// ntopngUp reports whether ntopng is answering on the loopback. Probing the
// port rather than checking for the binary means the UI reflects "you can
// click this", not "something is installed somewhere".
func (e *Engine) ntopngUp() bool {
	if time.Since(e.ntopChecked) < 15*time.Second {
		return e.ntopUp
	}
	e.ntopChecked = time.Now()
	c, err := net.DialTimeout("tcp",
		net.JoinHostPort("127.0.0.1", strconv.Itoa(ntopngPort)), 300*time.Millisecond)
	if err != nil {
		e.ntopUp = false
		return false
	}
	_ = c.Close()
	e.ntopUp = true
	return true
}

// wlanPresenceGrace is how long a wireless client may be missing from the
// station table before it stops counting as present. Long enough to ride out a
// roam or a power-save blip, short enough that a device which has actually gone
// leaves the list while the operator is still looking at it.
const wlanPresenceGrace = time.Minute

func (e *Engine) tick() {
	now := time.Now()

	// One dump per radio, merged, remembering which radio each station is on.
	// The radio matters beyond the label: a per-client link event has to be
	// sent to the control socket of the radio the client is actually
	// associated to, and sending it to the other one fails with a station
	// hostapd has never heard of.
	stations := map[string]*Station{}
	stationRadio := map[string]string{}
	for _, w := range e.cfg.WlanPorts {
		for mac, st := range StationDump(w) {
			stations[mac] = st
			stationRadio[mac] = w
		}
	}
	e.stationRadio = stationRadio
	fdb := BridgeFDB(e.cfg.Bridge, e.cfg.WANPort, e.cfg.WlanPorts)
	// ARP is the primary source for both address and port: it observes the
	// client directly, whereas the forwarding database depends on MAC learning
	// that ages out and, in practice, is often empty.
	// 20 minutes rather than 5: a device can be busy yet ARP-silent for a long
	// stretch, and expiring it early is what made a streaming phone read as
	// "no address yet". The IPv4 sampler refreshes anything actually sending.
	arp := e.learn.Table(20 * time.Minute)
	neigh := NeighTable(e.cfg.Bridge)
	macNames := e.learn.MACNames()
	names := e.learn.Names()
	policies := e.st.All()

	// The join. Presence is the union of "associated to the radio" and "seen on
	// a bridge port"; a DHCP lease is deliberately not part of it, because a
	// lease outlives the client that held it.
	type acc struct {
		medium, port string
		present      bool
	}
	merged := map[string]*acc{}
	// Remember who is associated right now, so a wireless client that has left
	// can be told from one that is merely quiet.
	for mac := range stations {
		e.lastAssoc[mac] = now.UnixMilli()
	}
	// Anything the bridge learned on the WAN port lives upstream. Those MACs
	// ARP too, and without this they would appear as clients of this box.
	wanSide := WANSideMACs(e.cfg.Bridge, e.cfg.WANPort)
	for mac := range stations {
		// The radio this station is actually on, not a fixed name: the port is
		// what a downlink tc filter attaches to, so with two radios serving,
		// naming the wrong one attaches the filter to an interface the client's
		// traffic never leaves by.
		merged[mac] = &acc{medium: "wifi", port: stationRadio[mac], present: true}
	}
	for mac, bp := range fdb {
		if a, ok := merged[mac]; ok {
			if a.port == "" {
				a.port = bp.Port
			}
			continue
		}
		merged[mac] = &acc{medium: bp.Medium, port: bp.Port, present: true}
	}
	// A device that is ARPing is present. ARP does not reveal the port (see
	// ARPSniffer), so a device known only from ARP is listed and can be
	// labelled and configured, but stays unshapeable until the forwarding
	// database or the station table says where it is -- a tc filter has to be
	// attached to a specific interface.
	//
	// Hosts on the WAN side also ARP, and they are not clients of this box, so
	// anything the forwarding database places on the WAN port is excluded.
	//
	// The port the learner OBSERVED is the authoritative test, not the
	// forwarding database. The bridge ages FDB entries out after ~300s while
	// the learner keeps bindings for 20 minutes, so relying on the FDB alone
	// let every host on the upstream LAN drift into the client list once its
	// FDB entry expired -- boa listed six of the house's devices as clients.
	//
	// A device is only a client of this box if its traffic arrived on a
	// DOWNSTREAM port. Anything seen on the WAN port lives upstream, and a
	// device whose port is unknown cannot be shaped anyway (a tc filter has to
	// attach to an interface), so listing it would be noise.
	downstream := map[string]bool{e.cfg.LanPort: true}
	for _, w := range e.cfg.WlanPorts {
		downstream[w] = true
	}
	for mac, sn := range arp {
		if sn.Port == e.cfg.WANPort || fdb[mac].Port == e.cfg.WANPort || wanSide[mac] {
			continue
		}
		if a, ok := merged[mac]; ok {
			a.present = true
			if a.port == "" && downstream[sn.Port] {
				a.port = sn.Port
			}
			continue
		}
		// A wireless client that is not in the station table is NOT here,
		// whatever the learner still remembers. Association is definitive: the
		// driver either holds the station or it does not.
		//
		// The learner keeps bindings for twenty minutes on purpose, so a device
		// that goes quiet keeps its policy and its label. That is right for
		// bookkeeping and wrong for presence -- a phone carried out of the
		// building read as a client of this box for the next twenty minutes,
		// and on an appliance whose whole subject is who is contending for
		// airtime, that is the one direction not to err in.
		//
		// One minute of grace, not zero: a station can briefly drop out of the
		// table across a roam or a power-save transition, and flapping the list
		// would be worse than a stale entry. The device stays LISTED either
		// way; only "present" changes.
		if e.cfg.IsWlan(sn.Port) {
			if last, seen := e.lastAssoc[mac]; !seen ||
				now.Sub(time.UnixMilli(last)) > wlanPresenceGrace {
				continue
			}
		}
		if !downstream[sn.Port] {
			continue // not on a port of ours: upstream, or not yet placed
		}
		medium := "wired"
		if e.cfg.IsWlan(sn.Port) {
			medium = "wifi"
		}
		merged[mac] = &acc{medium: medium, port: sn.Port, present: true}
	}
	// A device with stored policy that is not currently visible still appears,
	// so its configuration can be seen and edited before it reconnects.
	for mac := range policies {
		if _, ok := merged[mac]; !ok {
			merged[mac] = &acc{medium: "", port: "", present: false}
		}
	}

	clients := make([]Client, 0, len(merged))
	for mac, a := range merged {
		// ARP is preferred over the neighbour table: it is learned by observing
		// the client itself rather than inferred from the Pi's own traffic.
		ip := arp[mac].IP
		if ip == "" {
			ip = neigh[mac]
		}
		v6 := arp[mac].IPv6
		pol, hasPol := policies[mac]
		if !hasPol {
			pol = Policy{MAC: mac, Enabled: true}
		}
		// What the device calls itself, from its own mDNS announcements.
		//
		// The MAC-keyed binding comes first because it is the one that holds:
		// a device announces on whatever address it likes, including one this
		// box has never otherwise seen, and on a real network most of those
		// announcements are IPv6. Matching by address needs boa to already
		// know the address, which for an idle device it often does not.
		//
		// The address-keyed table is the fallback, for an announcement whose
		// sender could not be identified. Any of the device's addresses may
		// carry that binding, so all are tried.
		hostname := macNames[mac]
		if hostname == "" {
			hostname = names[ip]
		}
		if hostname == "" {
			for _, a := range v6 {
				if n := names[a]; n != "" {
					hostname = n
					break
				}
			}
		}
		// An operator-set label always wins; the announced name is only a
		// better default than a bare MAC.
		label := pol.Label
		if label == "" {
			label = hostname
		}
		if label == "" {
			label = mac
		}
		c := Client{
			MAC: mac, IP: ip, IPv6: v6, Hostname: hostname,
			Label: label, Medium: a.medium, Port: a.port,
			// Either family is enough to attach a filter to.
			Present: a.present, Shapeable: ip != "" || len(v6) > 0,
			Station: stations[mac], Policy: pol, LastSeen: now.UnixMilli(),
			SubCounters: map[string]Counters{},
		}
		// Which radio, and what that radio is doing. Only for a client actually
		// associated to one: a wired client has no radio, and a wireless client
		// the station table has lost is not on any radio right now, so claiming
		// one would be inventing a fact.
		if w := stationRadio[mac]; w != "" {
			c.RadioOn = e.radioOnFor(w)
			// Where it could be steered, from the radio it is actually on --
			// not from the primary. A client on the onboard radio and one on
			// the adapter are steered in opposite directions, and naming the
			// wrong source would build a neighbour report for the radio the
			// client is already sitting on.
			c.SteerTo = e.OtherRadio(w)
		}
		clients = append(clients, c)
	}
	// What CHANGED since the last tick, raised now that both the associations
	// and the labels for them are in hand.
	labels := make(map[string]string, len(clients))
	for _, c := range clients {
		labels[c.MAC] = c.Label
	}
	e.noteClientChanges(stationRadio, labels)
	e.noteRadioChanges()
	e.noteAPServing()
	// A steered client that never answered: the silence is the result, so it
	// is stated rather than left for the reader to infer from a missing line.
	e.reportMuteSteers()
	// AFTER the two watches above, so a radio that has drifted is reported as
	// having drifted before anything moves it back. The restore is loud, but
	// it is not the only thing that should have spoken.
	e.restoreChannels()

	sort.Slice(clients, func(i, j int) bool {
		// Present devices first, then by label, so the list does not reshuffle
		// as telemetry changes.
		if clients[i].Present != clients[j].Present {
			return clients[i].Present
		}
		return clients[i].Label < clients[j].Label
	})

	// Advance any ladder sweep BEFORE reconciling, so a level change reaches
	// the kernel on the tick it was decided rather than a second later.
	// Presence comes from the list just built: a device that left cannot be
	// measured, and continuing would record its absence as a rung.
	live := make(map[string]bool, len(clients))
	for _, c := range clients {
		live[c.MAC] = c.Present && c.Shapeable
	}
	e.sweep.Advance(now, sweepObserver{hist: e.hist, live: live})
	e.storeSweepResult()
	links, radios := e.player.Advance(now)
	for _, f := range links {
		go e.fireLink(f) // network I/O to hostapd; keep it off the tick
	}
	for _, f := range radios {
		go e.fireRadio(f) // rfkill and hostapd; likewise off the tick
	}

	if ready, _ := e.sh.Ready(); ready {
		for _, err := range e.sh.Apply(e.desired(clients)) {
			fmt.Printf("infinite-streaming-boa: shaping: %v\n", err)
		}
	}

	// Counters are read AFTER applying, so a class created this tick already
	// exists to be read rather than showing as absent for one frame.
	//
	// Downlink classes live on whichever port their client is attached to, so
	// each port in use is read once and cached rather than re-shelling out to
	// tc per client.
	upC := e.sh.ReadCounters(e.cfg.WANPort)
	downByPort := map[string]map[int]Counters{}
	readPort := func(dev string) map[int]Counters {
		if dev == "" {
			return nil
		}
		if c, ok := downByPort[dev]; ok {
			return c
		}
		c := e.sh.ReadCounters(dev)
		downByPort[dev] = c
		return c
	}

	// Each radio's channel, read BEFORE the lock below and looked up inside it.
	//
	// radioOnFor takes e.mu itself, and the section that follows HOLDS it.
	// Calling it in there deadlocks the tick against its own non-reentrant
	// mutex, and every HTTP handler then blocks behind the tick: measured
	// 2026-09-04, the box went on listening on :80 and answered nothing, from
	// localhost included. It also does a hostapd round trip on a cache miss,
	// which has no business happening once per client inside a lock.
	chanByIface := map[string]int{}
	for _, w := range e.cfg.WlanPorts {
		if r := e.radioOnFor(w); r != nil {
			chanByIface[w] = r.Channel
		}
	}

	e.mu.Lock()
	for i := range clients {
		c := &clients[i]
		c.Sweep = e.sweep.View(c.MAC)
		c.PatternRun = e.player.View(c.MAC)
		if m, ok := e.sh.MinorFor(c.MAC); ok {
			c.DownCounters = readPort(e.sh.DownPortFor(c.MAC))[m]
			c.DownCounters.ThroughputMbps = e.rate(
				fmt.Sprintf("d/%d", m), c.DownCounters.Bytes, now)
			c.UpCounters = upC[m]
			c.UpCounters.ThroughputMbps = e.rate(
				fmt.Sprintf("u/%d", m), c.UpCounters.Bytes, now)
		}
		for _, sub := range c.Policy.Sub {
			key := c.MAC + "/" + sub.ID
			m, ok := e.sh.MinorFor(key)
			if !ok {
				continue
			}
			d := readPort(e.sh.DownPortFor(key))[m]
			d.ThroughputMbps = e.rate(fmt.Sprintf("d/%d", m), d.Bytes, now)
			c.SubCounters[sub.ID] = d
		}
	}

	// Record the series before publishing, so a browser that loads in this
	// instant gets history consistent with the snapshot it also receives.
	for i := range clients {
		c := &clients[i]
		// Anything above a trickle counts as active. The same threshold the
		// sweep uses to call a client silent, for the same reason: below it a
		// device is doing background chatter, not work.
		if c.DownCounters.ThroughputMbps+c.UpCounters.ThroughputMbps > silentMbps {
			e.lastActive[c.MAC] = now.UnixMilli()
		}
		c.LastActiveMs = e.lastActive[c.MAC]
		// The link's ceiling alongside what actually crossed it. Taken from the
		// station table, so it is zero for a wired client and for a wireless one
		// that has gone -- which the chart draws as a gap rather than as a floor.
		var phyDown, phyUp float64
		if c.Station != nil {
			phyDown, phyUp = c.Station.TxPhyMbps, c.Station.RxPhyMbps
		}
		// Where it was attached, and on what channel. Only while PRESENT: a
		// listed device that has gone away keeps its port for display, and
		// recording that as the sample's adapter would draw an unbroken band
		// under a chart of a client that was not there.
		var sIface string
		var sChan int
		if c.Present {
			sIface = c.Port
			sChan = chanByIface[c.Port] // zero for a wired port, which has none
		}
		e.hist.Add(c.MAC, Sample{
			T:       now.UnixMilli(),
			Down:    c.DownCounters.ThroughputMbps,
			Up:      c.UpCounters.ThroughputMbps,
			Cap:     c.DownCounters.CapMbps,
			PhyDown: phyDown,
			PhyUp:   phyUp,
			Iface:   sIface,
			Channel: sChan,
		})
	}
	if e.rev%60 == 0 {
		e.hist.Prune(now)
	}

	e.rev++
	ready, reason := e.sh.Ready()
	burstOK, burstNote := e.sh.LossBurst()
	snap := Snapshot{
		Version:  e.cfg.Version,
		Revision: e.rev, ControlRevision: e.ctrlRev, Time: now.UnixMilli(),
		Clients: clients,
		Caps: Capabilities{
			Shaping: ready, Uplink: ready, Reason: reason,
			Radio:     LinkExists(e.cfg.PrimaryWlan()),
			Leases:    false, // transparent bridge: upstream owns DHCP
			WlanIface: e.cfg.PrimaryWlan(), WlanIfaces: e.cfg.WlanPorts,
			UplinkIf: e.cfg.WANPort,
			Adapter:  Radio(e.cfg.PrimaryWlan()),
			Ntopng:   e.ntopngUp(), NtopngPort: ntopngPort,
			Iperf: PortListening(iperfPort), IperfPort: iperfPort,
			// /proc rather than a dial, as for iperf3: glances binds a fixed
			// port on every address, so a listener in the table is the whole
			// answer and costs no connection.
			Glances: PortListening(glancesPort), GlancesPort: glancesPort,
			LinkControl: e.anyLinkControl(),
			LossBurst:   burstOK, LossBurstNote: burstNote,
			NamesLearned: len(names), NamesByMAC: len(macNames),
		},
		Notices:    e.notices(ready, reason),
		AdapterRun: e.player.View(BoxBinding),
	}
	e.snap = snap
	subs := make([]chan Snapshot, 0, len(e.subs))
	for ch := range e.subs {
		subs = append(subs, ch)
	}
	e.mu.Unlock()

	for _, ch := range subs {
		// Non-blocking: a slow consumer is dropped for this frame rather than
		// stalling the tick. It will receive the next full snapshot and cannot
		// drift, because snapshots are complete rather than incremental.
		select {
		case ch <- snap:
		default:
		}
	}
}

// storeSweepResult persists a finished sweep's ladder against the device that
// produced it, keyed by the service that was being streamed.
//
// Reported rather than best-effort-silent: a sweep costs minutes of a person's
// time and a device's playback, and losing the result to a write failure with
// nothing on the console is the expensive kind of quiet.
func (e *Engine) storeSweepResult() {
	mac, ladder, ok := e.sweep.TakeResult()
	if !ok {
		return
	}
	p, found := e.st.Get(mac)
	if !found {
		p = Policy{MAC: mac, Enabled: true}
	}
	p.PutLadder(ladder)
	// The device keeps its record -- that is the measurement history, and the
	// interface shows which service it came from -- but the BOX's ladder is
	// what every pattern is generated from, so a fresh sweep has to move it.
	// Reported rather than swallowed: a sweep that measured a ladder nothing
	// will use is an hour of streaming wasted silently.
	if err := e.lad.Put(ladder); err != nil {
		fmt.Printf("infinite-streaming-boa: sweep %s: measured ladder not stored: %v\n",
			mac, err)
	}
	p.Rev++
	if err := e.st.Put(p); err != nil {
		fmt.Printf("infinite-streaming-boa: sweep %s: ladder measured but NOT saved: %v\n",
			mac, err)
		return
	}
	rungs := make([]float64, 0, len(ladder.Rungs))
	for _, r := range ladder.Rungs {
		rungs = append(rungs, r.Mbps)
	}
	fmt.Printf("infinite-streaming-boa: sweep %s: saved ladder for %q: %v Mbps\n",
		mac, ladder.Service, rungs)
	// The ladder is operator-visible configuration, so the UI must resync
	// rather than wait for its next full reload.
	e.mu.Lock()
	e.ctrlRev++
	e.mu.Unlock()
}

// notices are operational truths the UI is required to surface. They exist
// because every one of them has, at some point, cost somebody an afternoon.
func (e *Engine) notices(ready bool, reason string) []Notice {
	var n []Notice
	if !ready {
		n = append(n, Notice{"error", "Traffic conditioning is not active: " + reason})
	}
	n = append(n, Notice{"info",
		"Wi-Fi airtime is shared. Conditioning is additive on top of a variable " +
			"radio baseline, so one client's traffic still affects another's " +
			"achievable rate no matter what these limits say."})
	if !LinkExists(e.cfg.LanPort) {
		n = append(n, Notice{"info", "No USB ethernet adapter detected (" + e.cfg.LanPort +
			"): wired clients will not appear until one is plugged in."})
	}
	return n
}

func (e *Engine) Snapshot() Snapshot {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.snap
}

func (e *Engine) Subscribe() (chan Snapshot, func()) {
	ch := make(chan Snapshot, 4)
	e.mu.Lock()
	e.subs[ch] = struct{}{}
	e.mu.Unlock()
	return ch, func() {
		e.mu.Lock()
		delete(e.subs, ch)
		e.mu.Unlock()
		close(ch)
	}
}

func (e *Engine) ControlRevision() uint64 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.ctrlRev
}

// FlushNames persists the learned name table. Called on shutdown so a restart
// does not blank every device back to a bare MAC: devices announce themselves
// only occasionally, so the gap before the next announcement can be minutes,
// and from outside that is indistinguishable from the feature being broken.
func (e *Engine) FlushNames() {
	if e.namesPath != "" {
		e.learn.SaveNames(e.namesPath)
	}
	if e.histPath != "" {
		e.hist.Save(e.histPath)
	}
}

// History exposes the per-client series for the API.
func (e *Engine) History() *History { return e.hist }
