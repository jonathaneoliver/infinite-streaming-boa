package pifi

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
	Bridge    string // br-lan
	WANPort   string // the port cabled to the existing network; uplink shaped here
	WlanPort  string // wlan0
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
	learn *Learner

	rev, ctrlRev uint64
	snap         Snapshot

	// prev is keyed "dev/minor" and holds the last byte count seen there.
	prev map[string]counterSample

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
		cfg:       cfg,
		sh:        NewShaper(cfg.WANPort, cfg.Bridge, managementPorts(cfg.Addr)),
		st:        NewStore(cfg.StatePath),
		pat:       NewPatternStore(patternsPathFor(cfg.StatePath)),
		learn:     NewLearner(cfg.Bridge, cfg.WlanPort, cfg.LanPort),
		prev:      map[string]counterSample{},
		demo:      newDemoFleet(),
		demoBytes: map[string]uint64{},
		hist:      NewHistory(),
		sweep:     &Sweeper{},
		player:    &Player{},
		subs:      map[chan Snapshot]struct{}{},
	}
}

func patternsPathFor(statePath string) string {
	return filepath.Join(filepath.Dir(statePath), "patterns.json")
}

func (e *Engine) Store() *Store     { return e.st }

// PatternStore holds the box's saved patterns. Beside policy.json rather than
// inside it: policies persist as a bare object keyed by MAC, and folding
// patterns in would change that file's shape and need a migration on every
// existing box for no benefit over a second small file.
func (e *Engine) PatternStore() *PatternStore { return e.pat }
func (e *Engine) Shaper() *Shaper   { return e.sh }
func (e *Engine) Sweeper() *Sweeper { return e.sweep }
func (e *Engine) Player() *Player   { return e.player }

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
		fmt.Printf("infinite-streaming-pifi: shaping unavailable: %v\n", err)
	}
	// Devices announce only occasionally -- on join, on wake, when services
	// change -- so an in-memory-only name table means every daemon restart
	// drops every client back to a bare MAC until the next announcement,
	// which can be many minutes away. From outside that is indistinguishable
	// from the feature breaking.
	// tmpfs, not the state directory: history must survive a daemon restart --
	// which happens on every deploy -- but writing a time series to the SD card
	// every second is how a Pi appliance kills its storage.
	e.histPath = "/run/infinite-streaming-pifi/history.json"
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
			fmt.Printf("infinite-streaming-pifi: passive learner stopped: %v\n", err)
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
	for _, p := range []int{sshPort, ntopngPort} {
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

func (e *Engine) tick() {
	now := time.Now()

	stations := StationDump(e.cfg.WlanPort)
	fdb := BridgeFDB(e.cfg.Bridge, e.cfg.WANPort, e.cfg.WlanPort)
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
	// Anything the bridge learned on the WAN port lives upstream. Those MACs
	// ARP too, and without this they would appear as clients of this box.
	wanSide := WANSideMACs(e.cfg.Bridge, e.cfg.WANPort)
	for mac := range stations {
		merged[mac] = &acc{medium: "wifi", port: e.cfg.WlanPort, present: true}
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
	// FDB entry expired -- pifi listed six of the house's devices as clients.
	//
	// A device is only a client of this box if its traffic arrived on a
	// DOWNSTREAM port. Anything seen on the WAN port lives upstream, and a
	// device whose port is unknown cannot be shaped anyway (a tc filter has to
	// attach to an interface), so listing it would be noise.
	downstream := map[string]bool{e.cfg.WlanPort: true, e.cfg.LanPort: true}
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
		if !downstream[sn.Port] {
			continue // not on a port of ours: upstream, or not yet placed
		}
		medium := "wired"
		if sn.Port == e.cfg.WlanPort {
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
		// announcements are IPv6. Matching by address needs pifi to already
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
			RTTAddedMs:  pol.Down.DelayMs + pol.Up.DelayMs,
			SubCounters: map[string]Counters{},
		}
		clients = append(clients, c)
	}
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
	e.player.Advance(now)

	if ready, _ := e.sh.Ready(); ready {
		for _, err := range e.sh.Apply(e.desired(clients)) {
			fmt.Printf("infinite-streaming-pifi: shaping: %v\n", err)
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
		e.hist.Add(c.MAC, Sample{
			T:    now.UnixMilli(),
			Down: c.DownCounters.ThroughputMbps,
			Up:   c.UpCounters.ThroughputMbps,
		})
	}
	if e.rev%60 == 0 {
		e.hist.Prune(now)
	}

	e.rev++
	ready, reason := e.sh.Ready()
	snap := Snapshot{
		Revision: e.rev, ControlRevision: e.ctrlRev, Time: now.UnixMilli(),
		Clients: clients,
		Caps: Capabilities{
			Shaping: ready, Uplink: ready, Reason: reason,
			Radio:     LinkExists(e.cfg.WlanPort),
			Leases:    false, // transparent bridge: upstream owns DHCP
			WlanIface: e.cfg.WlanPort, UplinkIf: e.cfg.WANPort,
			Ntopng: e.ntopngUp(), NtopngPort: ntopngPort,
			Iperf: PortListening(iperfPort), IperfPort: iperfPort,
			NamesLearned: len(names), NamesByMAC: len(macNames),
		},
		Notices: e.notices(ready, reason),
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
	p.Rev++
	if err := e.st.Put(p); err != nil {
		fmt.Printf("infinite-streaming-pifi: sweep %s: ladder measured but NOT saved: %v\n",
			mac, err)
		return
	}
	rungs := make([]float64, 0, len(ladder.Rungs))
	for _, r := range ladder.Rungs {
		rungs = append(rungs, r.Mbps)
	}
	fmt.Printf("infinite-streaming-pifi: sweep %s: saved ladder for %q: %v Mbps\n",
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
