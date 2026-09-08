# HTTP API

<!-- GENERATED FILE. Do not edit by hand.
     Every word below comes from daemon/internal/boa: the routes from the mux
     registrations in api.go, the prose from the Go doc comments on the handlers
     and the wire types. Edit those and regenerate:

         cd daemon && go test ./internal/boa/ -run TestAPIDocs -update

     TestAPIDocsUpToDate fails if this file and the source disagree, so it
     cannot go stale. -->

The daemon serves this API on port 80 of the appliance and embeds the Vue
interface behind it, so every endpoint here is same-origin with the UI.
Units are the human-facing ones throughout -- megabits, milliseconds, percent;
see `docs/DATA-CONTRACT.md` for where each number comes from.

## Endpoints

55 routes.

| Method | Path |
|---|---|
| GET | `/api/state` |
| GET | `/api/state/stream` |
| GET | `/api/health` |
| GET | `/api/history` |
| GET | `/api/bridge` |
| GET | `/api/events` |
| GET | `/api/events/stream` |
| GET | `/api/bridge/radios/{iface}/survey` |
| POST | `/api/bridge/radios/{iface}/channel` |
| POST | `/api/bridge/radios/{iface}/move-channel` |
| POST | `/api/bridge/radios/{iface}/deauth-all` |
| POST | `/api/bridge/radios/{iface}/link-all` |
| POST | `/api/bridge/radios/{iface}/power` |
| POST | `/api/bridge/radios/{iface}/ap` |
| POST | `/api/services/{name}` |
| POST | `/api/verbose` |
| POST | `/api/bridge/radios/{iface}/scan` |
| POST | `/api/bridge/radios/{iface}/profile` |
| POST | `/api/bridge/radios/{iface}/bssload` |
| POST | `/api/bridge/radios/{iface}/threshold` |
| POST | `/api/bridge/radios/{iface}/steer` |
| POST | `/api/bridge/radios/{iface}/gather` |
| POST | `/api/bridge/radios/{iface}/evict` |
| PATCH | `/api/devices/{mac}/policy` |
| POST | `/api/devices/{mac}/sub` |
| PATCH | `/api/devices/{mac}/sub/{id}` |
| DELETE | `/api/devices/{mac}/sub/{id}` |
| POST | `/api/devices/{mac}/sweep` |
| DELETE | `/api/devices/{mac}/sweep` |
| PUT | `/api/devices/{mac}/pattern` |
| DELETE | `/api/devices/{mac}/pattern` |
| POST | `/api/devices/{mac}/pattern/play` |
| DELETE | `/api/devices/{mac}/pattern/play` |
| GET | `/api/patterns` |
| GET | `/api/patterns/{name}` |
| POST | `/api/patterns/merge` |
| POST | `/api/patterns/scenario` |
| GET | `/api/bridge/pattern` |
| PUT | `/api/bridge/pattern` |
| POST | `/api/bridge/pattern/play` |
| DELETE | `/api/bridge/pattern/play` |
| PUT | `/api/patterns/{name}` |
| DELETE | `/api/patterns/{name}` |
| POST | `/api/devices/{mac}/pattern/select` |
| GET | `/api/config` |
| POST | `/api/config` |
| PUT | `/api/devices/{mac}/ladders/{service}` |
| DELETE | `/api/devices/{mac}/ladders/{service}` |
| POST | `/api/devices/{mac}/reset` |
| POST | `/api/devices/{mac}/link/deauth` |
| POST | `/api/devices/{mac}/link/disassoc` |
| POST | `/api/devices/{mac}/link/deadzone` |
| POST | `/api/devices/{mac}/link/steer` |
| POST | `/api/devices/{mac}/link/measure` |
| DELETE | `/api/devices/{mac}` |

### GET /api/state

_No description: `getState` has no doc comment._

### GET /api/state/stream

_No description: `stream` has no doc comment._

### GET /api/health

_No description: `health` has no doc comment._

### GET /api/history

getHistory serves the per-client throughput series.

A separate endpoint rather than part of the snapshot: including it in every
server-sent event, once a second, would multiply the stream roughly a
hundredfold to re-send data the client already has. The UI fetches this once
on load and appends from the live stream thereafter.

### GET /api/bridge

getBridge serves the box's own interface inventory: every port, its MAC and
addresses, and what each radio's access point is doing.

A separate endpoint rather than part of the snapshot, for the same reason
getHistory is one: the snapshot goes out once a second to every connected
browser, and this changes on the timescale of somebody plugging a cable in.
It also costs two hostapd round-trips and a station dump per radio, which has
no business running at 1 Hz whether or not anyone is looking at it.

### GET /api/events

_No description: `getEvents` has no doc comment._

### GET /api/events/stream

streamEvents streams the activity log as newline-delimited JSON, one object
per line, until the client goes away.

NDJSON rather than the server-sent events /api/state/stream uses, because the
consumers are different: that one feeds a browser, this one feeds
`curl -sN http://box/api/events/stream > run.ndjson` and whatever reads the
file afterwards. An SSE frame has to be unwrapped before it can be parsed,
and a capture format that needs unwrapping is one more step between a run and
an answer.

# Why this exists at all

The ring holds eventRing events, in memory, and is cleared by every restart.
Its own sizing note says a few hundred covers "several minutes of a device
flapping" -- which is precisely what a bounce or drop experiment is, times
however many clients are in it. Polling /api/events on a cursor works, but it
is a race against the ring that the operator has to run themselves. This is
that race, run by the box, at a rate the ring cannot outpace.

# What it will not do

It does not persist anything. A deploy still clears the ring and ends the
stream, so events raised while the daemon was down are gone -- but everything
already written to the capture file is safe, which is the difference that
matters. Deliberately no SD writes; see the note on eventLog.

### GET /api/bridge/radios/{iface}/survey

getSurvey reads a radio's airtime counters.

A GET even though it costs a subprocess, because it changes nothing. Note the
contract on the way out: this is the OPERATING channel's airtime, never a
survey of the band, and SurveyResult.Note carries that in the payload so a
caller cannot lose it. See Source L.

### POST /api/bridge/radios/{iface}/channel

postChannel moves a radio, and every client associated to it, to another
channel via an 802.11h channel switch announcement.

AP-WIDE, unlike every /api/devices action: there is no MAC here because the
blast radius is the whole radio. The interface says so before the button is
pressed; this refuses loudly if hostapd will not do it, because a channel
switch that silently did nothing would look identical to one a client simply
followed. See issue #122.

### POST /api/bridge/radios/{iface}/move-channel

postMoveChannel puts a radio on a chosen channel by taking it down and
bringing it back up there.

The working counterpart to postChannel. That one announces the move and lets
clients follow without reconnecting, which is the nicer behaviour and is
refused by both drivers on this box; this one drops the access point and
brings it back elsewhere, which works and is what most consumer routers
actually do. Clients are not told and must rediscover it.

### POST /api/bridge/radios/{iface}/deauth-all

postDeauthAll drops every station on a radio. AP-wide, same reasoning as
postChannel; the count returned is how many were there to drop.

### POST /api/bridge/radios/{iface}/link-all

postLinkAll applies a per-client link event to every station on a radio.

The AP-wide sibling of the drop and nudge buttons on a device card. Both are
ANNOUNCED -- the clients are told and reconnect knowing why, which is the
whole distinction from switching the radio off.

### POST /api/bridge/radios/{iface}/power

postRadioPower cuts or restores a radio's power, telling its clients nothing.

The one impairment here that is SILENT. Every other action announces itself:
a deauthenticated client knows it was thrown off and reconnects in a second
or two. A client whose access point loses power is told nothing at all and
has to notice the beacons stopped, which takes it tens of seconds of
believing it is still connected. That is the case a real power cut, a
tripped breaker or walking round a corner produces, and nothing else in this
codebase can imitate it.

`?on=0|1` sets it and leaves it. `?dur=N` cuts power for N seconds and
restores it -- the more useful form, since what matters is what a player does
DURING an outage and how it recovers, and a manual off/on pair makes the
duration whatever the operator's reflexes were.

### POST /api/bridge/radios/{iface}/ap

postAPEnabled takes this radio's access point down or brings it back, leaving
the radio powered.

A SEPARATE verb from power, not a mode of it, because the two impairments are
different in the way that matters: power is rfkill and the client is told
nothing, while this is hostapd closing the BSS with the transmitter still on,
so the departure is announced. Folding them into one control would hide the
only variable an operator is trying to change.

### POST /api/services/{name}

postService starts or stops one of the box's own observability services.

The name is looked up in an allowlist and never passed through to systemctl,
so a request cannot name a unit of its own choosing. See services.go.

### POST /api/verbose

postVerbose turns the activity log's verbose mode on or off.

A LIVE setting rather than a daemon flag, because of when it is wanted: in
the middle of watching a device misbehave. A switch that needs a restart is
one that gets turned on for the run that has already finished, and the
restart clears the very log it was turned on to read -- the ring is in
memory, by design (see events.go).

It changes what is LOGGED, not what is displayed. The ring holds 500 entries;
filtering at the browser would let capability elements from every association
push the events worth keeping out of the buffer, so a reader who turned
verbose on and off again would find the interesting lines already gone.

### POST /api/bridge/radios/{iface}/scan

postScan takes a radio out of service, scans its band, and puts it back --
optionally on the quietest channel it found (`?apply=1`).

A beaconing radio cannot survey other channels, so this is genuinely
disruptive: the BSS is torn down for a few seconds and its clients are
dropped. On a box serving two radios they land on the other band and come
back, which is what makes it affordable; on a single-radio box it is an
outage. Either way the cost is reported in the result as outage_sec.

Applying happens while the radio is still down, which is why it works on
hardware that refuses CHAN_SWITCH (issue #154): nothing is announced, the
access point simply reappears elsewhere.

### POST /api/bridge/radios/{iface}/profile

postRadioProfile applies a named PHY or power-save profile, restarting the
BSS. Every client on the radio is dropped: these parameters are advertised in
the beacon and negotiated at association, so an associated station cannot be
told about them.

### POST /api/bridge/radios/{iface}/bssload

postBSSLoad sets what a radio ADVERTISES about its own congestion, which is
not what it measures.

An impairment aimed at the client's DECISION rather than at its packets. The
BSS Load element carries a station count and a channel utilisation, and some
clients weigh both when choosing between access points -- so this is the one
control here that can offer a device a reason to move rather than ordering it
to. `on=0` withdraws the element entirely, which is not the same as
advertising zero.

Values may only be raised ABOVE what is really happening, and this clamps
rather than refuses. Overstating load pushes devices away, which is what a
genuinely busy access point does anyway; understating it pulls them in, and
that lands on neighbours nobody here can see or ask.

### POST /api/bridge/radios/{iface}/threshold

postThreshold sets the RTS or fragmentation threshold. The only radio
impairment here that costs nothing: live on the next frame, nobody dropped.

### POST /api/bridge/radios/{iface}/steer

postSteer asks clients to move to another radio via 802.11v.

A REQUEST, not an instruction: the decision stays with the client, and
whether a given phone honours it is exactly the behaviour worth testing.
`?mac=` steers one; omitted, it asks everyone on the radio.

`?insist=1` disassociates a client that will not go, which is what EVICT
means -- "get off this radio", with no claim about where it lands. Without it
a refusal is taken for an answer, which is what GATHER means, because a
gather names its destination and a forced client picks its own. See
steerMode: the same frame carries a different promise depending on which
control sent it, and conflating them is what made "gather to wlan-usb" put a
device on wlan0.

### POST /api/bridge/radios/{iface}/gather

postGather moves every client on the other radios onto this one and PINS them
there, by denying them everywhere else for a while.

A separate endpoint from steer rather than a flag on it, because it is a
different kind of act. A steer is a request the client may refuse; this
removes the alternatives and then moves the client, so there is nothing to
refuse. Sharing an endpoint would put two different promises behind one name.

`?pin=<sec>` sets how long the deny lists are held. See gather.go.

### POST /api/bridge/radios/{iface}/evict

postEvict empties one radio and denies it, so the departure sticks.

The mirror of postGather. Both remove a choice rather than making a request:
gather leaves the client one radio it may join, evict leaves it every radio
but one. Neither is refusable, which is the point, and neither claims to be a
measurement of what the device would have chosen.

### PATCH /api/devices/{mac}/policy

_No description: `patchPolicy` has no doc comment._

### POST /api/devices/{mac}/sub

_No description: `postSub` has no doc comment._

### PATCH /api/devices/{mac}/sub/{id}

_No description: `patchSub` has no doc comment._

### DELETE /api/devices/{mac}/sub/{id}

_No description: `deleteSub` has no doc comment._

### POST /api/devices/{mac}/sweep

startSweep begins measuring a device's ladder for one service.

The device must be streaming that service already: the sweep's opening level
is an unconditioned observation, and with nothing playing it measures nothing
and says so rather than reporting an empty ladder.

### DELETE /api/devices/{mac}/sweep

stopSweep abandons a running sweep. The device returns to stored policy on
the next tick; nothing needs unwinding because nothing was written.

### PUT /api/devices/{mac}/pattern

putPattern stores a device's timeline. Storing is not playing.

### DELETE /api/devices/{mac}/pattern

deletePattern drops the stored timeline, and stops it if it is playing.

Stopping is not optional here: leaving a run driving a pattern that no longer
exists would condition the device from an object the UI cannot show.

### POST /api/devices/{mac}/pattern/play

playPattern starts the stored timeline, or resumes a paused run from where it
stopped.

### DELETE /api/devices/{mac}/pattern/play

stopPattern ends a run. The device returns to stored policy on the next tick;
nothing needs unwinding because nothing was written.

### GET /api/patterns

listPatterns returns the built-ins and every saved pattern.

Built-ins are GENERATED per request rather than stored, so they always
describe the ladder the device has now: re-sweep it and they reshape
themselves. That is also why they cannot be edited in place -- see
PatternStore.Put.

### GET /api/patterns/{name}

getPattern resolves ONE pattern to its concrete timeline, without selecting
it.

A built-in has no stored keyframes -- it is generated from the device's
ladder on demand -- so there was previously no way to see its rates except by
selecting it onto a device, which changes what that device is set to. Reading
something must not have the side effect of applying it: cloning a pattern to
edit a copy, or simply looking at what a built-in would do, are both reads.

mac and service pick the ladder for a built-in, and stretch scales the
result, exactly as they do for the list.

### POST /api/patterns/merge

mergePatterns lays several patterns over one another and returns the result
WITHOUT saving it.

A preview rather than a write, for two reasons. The operator has to name the
thing, and they will name it better once they can see what it turned out to
be -- a merge whose sources both drove the rate is a merge that did nothing,
and that is worth noticing before it acquires a name and a place in the
library. And saving goes through the existing PUT, so a merged pattern is
stored by exactly the same path as a hand-built one and cannot drift from it.

Order matters and is the caller's: where two sources drive the same axis, the
first in this list wins. See MergePatterns.

### POST /api/patterns/scenario

playScenario starts one pattern on several devices at the SAME instant.

Two devices given the same pattern from two separate clicks are offset by
however long the operator took, and a comparison between different moments of
the same pattern is not a comparison. This is the endpoint for "the same
stimulus, at the same time, to both of them, and see which reacted first".

Body:

	{"macs": ["aa:..","bb:.."]}                 each device's own stored pattern
	{"macs": [...], "pattern": "square_wave"}   one named pattern on all of them

Optional "service" and "stretch" resolve a built-in exactly as playing one on
a single device does, so a scenario and a card produce the same pattern.

There is no matching stop: DELETE on any member's play already ends the whole
group, because Player.Stop takes a run's group with it.

### GET /api/bridge/pattern

getAdapterPattern returns the box's radio timeline, or an empty one.

RECONCILED against the radios the box has now, and saved back when that
changes anything. An interface name here is bound to a USB socket, so
swapping a dongle renames it and leaves the pattern pointing at something
gone -- the events survive, are still sent at play time, and are skipped one
by one, while the editor draws no lane for them at all. See patternradios.go.

Done on the read path rather than only at startup: the daemon does restart
when the radio set changes, so startup would usually be enough, and "usually"
is how a pattern ends up quietly naming a radio that left weeks ago.
Idempotent, so repeating it costs nothing.

### PUT /api/bridge/pattern

putAdapterPattern stores the box's radio timeline.

Validated here as well as at play time, so an impossible pattern is refused
while the operator is still looking at the editor that produced it -- an
outage below the floor being the one that actually happens.

### POST /api/bridge/pattern/play

playAdapterPattern runs the box's radio timeline.

Bound to BoxBinding rather than a MAC, so every verb the Player already has
applies to it -- including the scenario grouping, which is how a radio
outage and a client's ladder walk end up on one clock.

### DELETE /api/bridge/pattern/play

stopAdapterPattern ends the box run.

Unlike a device run, forgetting is not enough: a radio taken down by a
pattern stays down until its own restore fires, and a stop that left the AP
off the air would be #182 by another route. RadioOutage owns that restore and
clears the #203 marker, so the wait is bounded -- but the operator is told,
because a radio that comes back seconds after a stop looks like a fault.

### PUT /api/patterns/{name}

savePattern stores an authored pattern under a name.

Editing a built-in and saving it is a "save as": the name in the path must be
a new one, because a built-in is derived and a frozen copy wearing its label
would make the same name mean different things on different boxes.

### DELETE /api/patterns/{name}

_No description: `deleteSavedPattern` has no doc comment._

### POST /api/devices/{mac}/pattern/select

selectPattern loads a named pattern onto a device.

Selecting is not playing: it fills the device's pattern slot, and
POST /pattern/play starts it. Keeping them separate means the list can be
changed without conditioning traffic the moment a row is ticked.

A built-in is resolved to concrete rates HERE, at selection, and the device
stores the result. So a device keeps running the shape it was given even if
the ladder is re-swept underneath it -- re-select to pick up the new one.
The alternative, resolving on every tick, means a sweep silently rewrites a
pattern mid-run and the trace no longer matches what was requested.

### GET /api/config

getConfig exports every device's operator intent as one document.

### POST /api/config

postConfig imports a configuration.

Validated in full before anything is written, and written in one store
operation, so the box either ends up matching the document or is left exactly
as it was. Import is not a merge of individual fields: a device present in
the document replaces that device's policy wholesale, because a configuration
describes a state rather than a change.

### PUT /api/devices/{mac}/ladders/{service}

putLadder writes a ladder by hand, or corrects a measured one.

Any hand-edited ladder becomes "typed" regardless of what it was before.
Leaving it labelled "measured" after an operator moved a rung would attach
the authority of a measurement to a number nobody measured.

### DELETE /api/devices/{mac}/ladders/{service}

_No description: `deleteLadder` has no doc comment._

### POST /api/devices/{mac}/reset

resetDevice clears conditioning but keeps the device's label and sub-class
definitions, because the common case is "give me a clean baseline again",
not "forget everything I set up".

### POST /api/devices/{mac}/link/deauth

_No description: `linkDeauth` has no doc comment._

### POST /api/devices/{mac}/link/disassoc

_No description: `linkDisassoc` has no doc comment._

### POST /api/devices/{mac}/link/deadzone

linkDeadzone holds a client off the AP for ?dur=<seconds> (default 10) --
a sustained outage, long enough to actually stall a stream, unlike a single
deauth. See issue #135.

### POST /api/devices/{mac}/link/steer

linkSteer asks ONE client to move to the box's other radio (802.11v).

The per-client counterpart to the radio-wide steer on the bridge diagram, and
the more useful of the two: moving every client at once changes the whole
box, where the question worth asking is usually "what does THIS phone do when
told to move".

A REQUEST, not an instruction, exactly as the radio-wide one is: the client
decides, and whether a given device honours a transition request is the
behaviour under test. A refusal is therefore a RESULT, not an error -- what
this reports is whether the request was delivered.

Both radios are resolved here rather than taken from the caller. The source
is the radio the client is actually associated to, so a client on either band
is steered the right way round, and the target is whatever else is serving.

### POST /api/devices/{mac}/link/measure

linkMeasure asks one client to measure the box's OTHER radios and report what
it hears (802.11k beacon request). See #228.

Returns how many measurements were REQUESTED, not how many came back: the
reports arrive asynchronously through the monitor connection and land on the
client's own record, exactly as a steer's answer does. A client is free to
decline, and a decline is itself reported.

### DELETE /api/devices/{mac}

forgetDevice removes a device's stored configuration entirely.

A device with stored policy is listed even when absent, so its settings can
be prepared before it reconnects. That is useful until something transient
leaves a row behind -- a test rig, a guest, a randomised MAC that will never
return -- and then the list only grows. Reset clears conditioning but keeps
the row; this drops it.

A device that is still on the network will simply reappear, unconfigured,
which is the correct outcome: boa is describing what it sees.

## Payload types

The response shapes the interface consumes, and every type reachable from
them. Field names are what appears on the wire.

### Snapshot — GET /api/state

Snapshot is the whole server state, delivered complete on every SSE event.

Sending full snapshots rather than deltas is deliberate: a client that misses
an event cannot drift, it simply renders the next one. The two revisions are
separate because they answer different questions --

	Revision        changes every tick (telemetry moved)
	ControlRevision changes only when a policy was written

The UI resyncs slider positions only when ControlRevision changes, so live
telemetry updates never yank a control out from under the operator's cursor.

**`version`** `string`
> Version is the running build's version string (see main.version). The UI
> shows it so an operator can tell which build a box is on.

**`revision`** `uint64`

**`control_revision`** `uint64`

**`time`** `int64`

**`caps`** `Capabilities`

**`clients`** `[]Client`

**`notices`** `[]Notice` _(omitted when empty)_
> Notices are operational truths the UI must surface. Severity is carried
> in the data rather than inferred from the wording, because where a
> notice belongs on screen depends on whether it is actionable: an error
> buried at the foot of the page is worse than clutter at the top.

**`adapter_run`** `*PatternView` _(omitted when empty)_
> AdapterRun is the box's own timeline, when one is playing. Carried in the
> snapshot for the same reason a device's run is: so the editor can draw a
> moving playhead without polling a second endpoint.

### BridgeInfo — GET /api/bridge

BridgeInfo is the whole answer for the bridge view.

**`bridge`** `string`

**`ifaces`** `[]IfaceInfo`

**`notes`** `[]Notice` _(omitted when empty)_
> Notes are stated limitations, not errors -- chiefly "this radio's
> clients are not conditioned". They exist because the alternative is an
> operator discovering it from an empty device list.

**`scans`** `map[string]ScanSummary` _(omitted when empty)_
> Scans is the last band scan per radio, kept so the interface can colour
> the channel controls by what was actually heard.
>
> Served from the daemon rather than held in the browser: a measurement
> that vanishes on a page reload is one people stop trusting, and two
> people looking at the same box should see the same colours. Only the
> per-channel summary travels here, never the access point list -- that is
> hundreds of entries on a busy band, and this payload is polled.

**`air`** `map[string]AirView` _(omitted when empty)_
> Air is how contested each radio's own channel is, resolved from
> whichever scan measured that channel most recently -- which is usually
> NOT a scan by that radio. See AirView, and airview.go for why.
>
> Entries are absent rather than zeroed for a channel nobody has scanned.

**`bss_load`** `map[string]BSSLoadState` _(omitted when empty)_
> BSSLoad is what each radio has been told to ADVERTISE about its own
> congestion, and the floor of what it is really doing. See bssload.go.
>
> Every other impairment here acts on the link; this one acts on what a
> client is told, so the honest figure travels beside the claim and the
> interface shows both. A control that can lie is only safe while the truth
> is next to it.

**`read_age_ms`** `int` _(omitted when empty)_
> ReadAgeMs is how long ago this view was actually built, in milliseconds.
>
> It is built on a timer rather than per request, so it can be a couple of
> seconds old normally and minutes old while a radio holds rtnl_lock
> through a firmware reload. A view that describes a moment must say which
> moment, or a stale one passes for current -- and during a radio recovery
> that is exactly when it would mislead.

### Event — GET /api/events

Event is one thing that happened, already phrased for a human.

The text is composed at the point the event is raised rather than assembled
in the interface, because that is where the before-and-after are both still
in hand: "moved wlan-usb -> wlan0" needs the old value, which no later reader
has.

**`seq`** `uint64`
> Seq strictly increases, so a reader can ask for what it has not seen
> without depending on timestamps, which can repeat within a millisecond.

**`at`** `int64`

**`kind`** `string`

**`text`** `string`

**`mac`** `string` _(omitted when empty)_
> MAC and Iface are set when the event is about a particular client or
> radio, so the interface can filter without parsing the text.

**`iface`** `string` _(omitted when empty)_

### Sample — GET /api/history (clients[mac][])

Sample is one throughput observation, in the units the UI displays.

**`t`** `int64`

**`down`** `float64`

**`up`** `float64`

**`cap`** `float64`
> Cap is the downlink cap the kernel was enforcing at this instant, 0 for
> unlimited.
>
> Recorded per sample rather than read from the policy when the chart is
> drawn, because a pattern moves the cap while the chart is being watched:
> the current value says nothing about what was in force when the player
> reacted three minutes ago. Lining a player's behaviour up against the cap
> that caused it is the entire purpose of this box, and it cannot be done
> from a single number.
>
> It is the ENFORCED cap read back from tc, not the requested one, for the
> same reason DownCounters.CapMbps is: the chart should show what the
> kernel believed, so a shaping failure appears as a flat line at the old
> value rather than being papered over by the value we asked for.

**`phy_down`** `float64` _(omitted when empty)_
> PhyDown and PhyUp are the negotiated PHY rates at this instant, Mbit/s:
> the rate the radio was sending to this client and receiving from it.
>
> Recorded per sample for the same reason Cap is. The PHY rate is the most
> volatile number this box reports -- rate control re-picks an MCS per
> frame, and a client that re-associates can sit several hundred Mbit/s
> lower for minutes afterwards -- so the current value says nothing about
> the ceiling that applied when a player made a decision. A throughput
> trace that fell at 14:32 means something quite different depending on
> whether the link's ceiling fell with it.
>
> Per DIRECTION, because they differ: measured here, one client reported
> 1200.9 down and 1020.6 up at the same moment. Zero for a wired client and
> for a wireless one the station table has lost.

**`phy_up`** `float64` _(omitted when empty)_

**`air`** `float64` _(omitted when empty)_
> Air is the percentage of wall clock the radio spent transmitting to and
> receiving from this client over the tick that produced this sample.
>
> Recorded per sample for the same reason Cap and the PHY rates are, and
> with more force: airtime is what a client COSTS the radio, and it does
> not follow throughput. Measured 2026-09-07, one client held 46% of
> wlan-usb while moving 34 Mbit/s and another held 77% while moving 505 --
> six times the airtime per bit, because small unaggregated frames pay
> preamble, IFS and ACK that an A-MPDU amortises. On a throughput trace the
> first client merely looks quiet.
>
> An OCCUPANCY, never a share. The values across a radio's clients do not
> sum to 100 and the remainder is not idle -- see Engine.airtimePct.
>
> Zero when the driver cannot report it, which is NOT the same as an idle
> client, and the difference cannot be carried here: a pointer costs 8
> bytes plus an allocation against the 7.4MB budget computed below, for a
> fact that is a property of the RADIO rather than of the moment. It lives
> on IfaceInfo.AirtimePerClient instead, and the interface consults that
> before drawing anything from this. The Pi's onboard brcmfmac reports no
> per-station airtime at all, so its clients would otherwise be drawn as a
> radio full of perfectly idle devices.

**`iface`** `string` _(omitted when empty)_
> Iface is the bridge port this client was attached to at this instant,
> and Channel is the channel that radio was on.
>
> Recorded per sample for the same reason Cap and the PHY rates are: the
> client's CURRENT adapter says nothing about which one carried the
> traffic being looked at. A trace that fell at 14:32 means something
> entirely different if the client moved from 5GHz to 2.4GHz at 14:31, and
> on a two-radio box that is a routine thing to have happened. The
> interface draws these as a band under the chart on the same x-axis, so
> the move and its consequence are read together rather than inferred.
>
> Channel travels alongside because the adapter alone is not the whole
> story: a radio moved from 149 to 40 mid-trace is the same interface and
> a different link, and #195 is a reminder of how easily those two are
> conflated.
>
> EMPTY when the client is not attached -- a listed device that has gone
> away keeps its policy and its history, and the gap is the fact. Drawing
> it as a gap rather than as a continuation of the last adapter is what
> stops a chart implying a client was on a radio it had left.

**`channel`** `int` _(omitted when empty)_

### ConfigExport — GET /api/config

ConfigExport is the whole box's operator intent in one document: every
device's conditioning, sub-classes, ladders and pattern.

It exists because a profile spans four endpoints -- PATCH /policy for the
shapes, PUT /pattern, one PUT per ladder and one POST per sub-class -- and
reassembling a device by hand from those is both tedious and easy to get
half-right. A ladder in particular costs an hour of a real device streaming
real content, and the box is not the system of record: reflashing replaces
the whole image.

Telemetry is deliberately absent, for the same reason the Store never
persists it: it rebuilds itself on restart and means nothing on another box.

**`version`** `int`

**`exported_at`** `int64`
> ExportedAt is informational. Import never reads it -- a configuration is
> applied because the operator asked, not because it is recent.

**`patterns`** `[]Pattern` _(omitted when empty)_
> Patterns is the box's saved pattern library, sorted by name. Built-ins
> are absent on purpose: they are generated from whichever ladder a device
> has, so storing them would freeze a shape that is meant to track the
> content, and restoring one onto another box would describe that box's
> ladders wrongly.

**`devices`** `[]Policy` _(omitted when empty)_
> Devices is sorted by MAC. Go randomises map iteration, and these
> documents are meant to be committed to a repository and diffed, where
> unstable ordering turns a no-op export into a whole-file change.
> Devices is READ but no longer written; see ExportConfig for why. Kept so
> a version 1 document, which carried them, still restores what it can.

**`ladder`** `*Ladder` _(omitted when empty)_
> Ladder is the box's one measured ladder: the only genuinely expensive
> thing here, an hour of a real device streaming real content, and the
> reason this document exists at all.

### SurveyResult — GET /api/bridge/radios/{iface}/survey

SurveyResult is one radio's airtime reading.

**`iface`** `string`

**`operating_freq_mhz`** `int` _(omitted when empty)_
> OperatingFreqMHz is what the radio is actually tuned to, from
> `iw dev <if> info`. Authoritative; the survey's own labels are not.

**`channels`** `[]SurveyChannel`

**`mislabelled`** `bool` _(omitted when empty)_
> Mislabelled records that the driver attributed the airtime to a
> frequency the radio is not on. Surfaced rather than quietly corrected:
> silently rewriting a driver's output is how a wrong reading becomes
> invisible when the driver is later fixed.

**`note`** `string`
> Note is always populated. `iw survey dump` prints a block for every
> channel the phy knows and this is emphatically NOT a band scan; saying so
> in the payload keeps the caller from ranking a table of zeroes.

### patternEntry — GET /api/patterns (patterns[])

patternEntry is one row of the pattern list.

**`name`** `string`

**`builtin`** `bool`

**`dur_sec`** `float64`

**`keys`** `int`

**`loop`** `bool`

**`selected`** `bool` _(omitted when empty)_
> Selected marks the pattern currently loaded on the device the list was
> asked for. The list is single-select: a device runs one pattern, so the
> UI needs to know which row is the live one.

**`ladder_service`** `string` _(omitted when empty)_
> LadderService names the ladder a built-in was generated from, and Ladder
> says whether that ladder was real. A pattern built from the stand-in
> ladder is a plausibly-shaped test rather than a test of this content, and
> the difference must be visible rather than inferred from the rates.

**`ladder`** `string` _(omitted when empty)_

**`unavailable`** `string` _(omitted when empty)_
> Unavailable explains why a built-in could not be generated, instead of
> omitting the row. A pattern that silently vanishes from a list reads as a
> missing feature; one that says why reads as a thing to fix.

### APStatus

APStatus is what a hostapd-served radio is doing right now.

Nothing here is known from configuration: SSID, channel, band and width are
baked into /etc/hostapd/boa.conf at IMAGE BUILD time from .env, so the only
way to learn them on a running box is to ask hostapd. See Source K.

**`ssid`** `string` _(omitted when empty)_

**`bssid`** `string` _(omitted when empty)_

**`country`** `string` _(omitted when empty)_
> Country is the GLOBAL regulatory domain from `iw reg get`. hostapd
> reports no country code at all despite being configured with one, so
> attributing this to a particular radio is a display choice rather than
> something the radio told us.

**`channel`** `int` _(omitted when empty)_

**`freq_mhz`** `int` _(omitted when empty)_

**`width_mhz`** `int` _(omitted when empty)_
> WidthMHz is DERIVED -- hostapd has no width field. See apWidth.

**`mode`** `string` _(omitted when empty)_
> Mode is the highest enabled generation, e.g. "802.11ax".

**`enabled`** `bool`
> Enabled means hostapd reports state=ENABLED AND the interface is up.
>
> BOTH, because hostapd's state alone is not the whole answer and once
> claimed to be. MEASURED 2026-09-06: NetworkManager handed wlan-usb2 from
> managed to unmanaged, wpa_supplicant deinit'd the interface on its way
> out, and hostapd logged INTERFACE-DISABLED -- while its STATUS went on
> answering state=ENABLED for twenty minutes. Nothing was on air, the SSID
> vanished from every client's list, and the rack showed the radio serving
> the whole time. hostapd's state survives the interface being taken out
> from under it, so a radio reported on the strength of that state alone is
> a radio reported on hostapd's memory of what it last set up.

**`link_down`** `bool` _(omitted when empty)_
> LinkDown records exactly that disagreement: hostapd says ENABLED, the
> kernel says the interface is down. Separate from a plain "not serving"
> because the two need different actions -- a disabled AP is enabled again,
> whereas this one needs hostapd restarted, and an operator who cannot tell
> them apart will press the wrong control and conclude the box is broken.

**`stations`** `int`

**`beacon_int_ms`** `int` _(omitted when empty)_
> BeaconIntMs and DTIMPeriod are the power-save timing knobs. Shown
> because a phone's downlink behaviour between segment fetches is governed
> by them and by nothing else visible in this interface.

**`dtim_period`** `int` _(omitted when empty)_

### AirView

AirView is how contested one radio's channel is, from whichever scan
measured it -- not necessarily a scan by that radio.

That indirection is the point. On mt7921u a scan means taking the access
point down, so a 5GHz radio can only describe its own channel by dropping its
clients. The onboard brcmfmac radio scans BOTH bands while it keeps serving,
so one free scan there answers for every radio on the box. From records which
radio actually measured it, because "wlan-usb2's channel is at 2%" is a
different claim depending on who heard it and from where.

**`channel`** `int`

**`from`** `string`

**`at`** `int64`

**`util_pct`** `float64`
> UtilPct is the measured airtime on this channel, 0-100, as the loudest
> reporting neighbour saw it. UtilKnown is false when nobody on the channel
> advertised BSS Load -- which is NOT an idle channel, and must not render
> as 0%.
>
> VERIFIED against our own per-client counters 2026-09-07, the one
> instrument here checked against iperf3. Over the same 4.1s window our
> stations accounted for 78.04% of wlan-usb while two independent
> neighbours reported channel 40 at 69.8% and 83.9% -- bracketing it. At
> idle the same neighbours read 9.8% and 20.0% against ~0% of our own, so
> the figure tracks load in the right direction and the right magnitude.
> See DATA-CONTRACT Source O.

**`util_known`** `bool`

**`util_min_pct`** `float64` _(omitted when empty)_
> UtilMinPct and UtilReporters are the spread and the sample size behind
> UtilPct, which is the HIGHEST of them.
>
> The neighbours on one channel do not agree, and by a lot: measured
> 2026-09-07, five access points on channel 2 reported 19% to 33%, and
> three on channel 40 reported 9% to 20%. They sit in different rooms and
> genuinely hear different amounts of the same medium, so the spread is
> physical rather than noise. Showing only the maximum hid an 11-14 point
> disagreement behind a number that looked precise.
>
> UtilReporters is the confidence: one faint access point describing its own
> corner of the world deserves to read differently from five that agree.

**`util_reporters`** `int` _(omitted when empty)_

**`loudest_dbm`** `float64` _(omitted when empty)_
> LoudestDBm is the strongest NEIGHBOUR heard on this channel, ours
> excluded. Zero when nothing was heard, which for a scan is real evidence
> of a clear channel rather than a gap -- a scan lists everything audible.

**`ours_dbm`** `float64` _(omitted when empty)_
> OursDBm is this radio's own access point as the SCANNING radio heard it.
> Absent for the radio that took the scan, since a radio cannot hear
> itself, and absent when no scan has covered it yet.

**`ours_known`** `bool` _(omitted when empty)_

### BSSLoadState

BSSLoadState is one radio's override and the floor it may not go below.

**`on`** `bool`

**`stations`** `int`

**`util_pct`** `float64`

**`floor_stations`** `int`
> FloorStations and FloorUtilPct are what is REALLY there, and the lowest
> the controls may be set to.
>
> The constraint is the whole safety property. Overstating load pushes
> devices away, which is what a genuinely busy access point does anyway;
> understating it PULLS them in, and that lands on neighbours' equipment we
> do not own and cannot observe. A box that can lie should only ever lie in
> the direction that costs other people nothing.
>
> The utilisation floor is our own measured airtime -- the figure verified
> against iperf3 in Source T -- because the channel is at least as busy as
> we are making it. It is a lower bound rather than the true utilisation,
> which would also include neighbours we can only see when we scan.

**`floor_util_pct`** `float64`

**`floor_known`** `bool`
> FloorKnown is false where the driver cannot report per-client airtime, so
> the utilisation floor is a guess rather than a measurement. The onboard
> brcmfmac radio is the case: no per-station duration counters at all.

### BeaconReport

BeaconReport is one client's own measurement of one BSS.

**`bssid`** `string`
> BSSID measured, and the interface of ours that owns it when it is ours.
> A client may report a neighbour's BSS too; naming ours is what makes the
> number comparable to the radio row beside it.

**`iface`** `string` _(omitted when empty)_

**`channel`** `int`
> Channel the measurement was taken on, as the CLIENT reports it rather
> than as we asked -- a client that measured somewhere else has told us
> something worth seeing rather than something to normalise away.

**`rcpi`** `int`
> RCPI as received, and the dBm it converts to. Both, because the
> conversion is lossy in the sense that matters: 255 means "not available"
> and would otherwise arrive as a confident 17.5 dBm.

**`signal_dbm`** `int`

**`has_signal`** `bool`

**`rsni_db`** `float64` _(omitted when empty)_
> RSNI in the same units the standard uses: 0.5 dB steps from -10 dB.

**`has_rsni`** `bool` _(omitted when empty)_

**`at_ms`** `int64`

**`requested`** `bool`

### Capabilities

Capabilities tells the UI which parts of the system are actually working, so
a missing kernel module degrades into a clear banner instead of controls that
silently do nothing.

**`shaping`** `bool`

**`uplink`** `bool`

**`radio`** `bool`

**`leases`** `bool`

**`wlan_iface`** `string`

**`wlan_ifaces`** `[]string` _(omitted when empty)_
> WlanIfaces is EVERY radio the daemon watches. WlanIface stays as the
> first of them so older readers keep working; a box serving two radios
> reports both here, and a client on any of them is conditioned.

**`adapter`** `RadioInfo`
> Adapter names the radio actually serving the AP and, when it is a USB
> one, the speed it NEGOTIATED. Both matter to an operator: which radio is
> live is otherwise a guess when two are plugged in, and a USB 3 adapter
> that quietly enumerated at USB 2 looks identical everywhere else while
> delivering a sixth of the throughput. Distinct from Radio above, which
> only says whether an interface is present at all.

**`uplink_if`** `string`

**`ntopng`** `bool`
> Ntopng reports whether ntopng is actually ANSWERING, not merely
> installed. The UI hides its deep links otherwise, because a reflashed
> image without the prebuilt artifact would otherwise show dead links.

**`ntopng_port`** `int`

**`iperf`** `bool`
> Iperf reports whether the iperf3 server is LISTENING, so a script can
> check the box can measure its own link without parsing the notices.
> It measures the unshaped link only; see the unit file for why.

**`iperf_port`** `int`

**`glances`** `bool`
> Glances reports whether the glances web UI is LISTENING. Same reasoning
> as Ntopng: the header link is hidden rather than shown pointing at a
> refused connection, so an image built without it degrades to silence.

**`glances_port`** `int`

**`services`** `[]ServiceInfo` _(omitted when empty)_
> Services are the box's own observability processes and whether each is
> running, so the header can offer a switch beside each link.
>
> Distinct from the two booleans above, which report whether a PORT is
> listening and decide whether a link is worth showing at all. This lists
> every service the box will start or stop, running or not -- because a
> stopped service still needs its own start button, and gating that on the
> service being up would remove the only control that could bring it back.
>
> Cheap enough for the 1Hz snapshot because the reading is cached and
> invalidated on a press; see serviceStates.

**`link_control`** `bool`
> LinkControl reports whether per-client link events (deauth/disassoc) can
> be driven -- i.e. hostapd is serving the AP and exposing its control
> socket. False on the onboard/NetworkManager radio, which offers no such
> control, so the UI hides the actions rather than offer a dead button.
> See hostapd.go and issue #135.

**`verbose`** `bool`
> Verbose is whether the activity log is also carrying the received
> management frames that are context rather than events -- capability
> elements on every association, subtypes nothing acts on.
>
> Reported so the control can show its own state: a switch that does not
> say whether it is on is one the operator has to press to find out, and
> pressing it is the thing they were trying to decide about. See
> handleMgmtFrame for what it gates and what it does not.

**`names_learned`** `int`
> NamesLearned is how many address-to-name bindings mDNS has yielded, and
> NamesByMAC how many of the MAC-keyed bindings that actually label a
> client. Zero means nothing is being heard; a healthy number while a
> client still shows a MAC means that device simply has not announced
> itself. Without this the two cases are indistinguishable from outside
> the box. They are separate because the MAC-keyed count is the one that
> says the filtered capture socket is working -- it stays zero if that
> failed to open, while the address count keeps climbing.

**`names_by_mac`** `int`

**`reason`** `string` _(omitted when empty)_

**`loss_burst`** `bool`
> LossBurst reports whether this kernel's netem accepts a Gilbert-Elliott
> loss model, asked at startup rather than assumed. False disables the
> burst control with LossBurstNote as the reason -- the alternative is an
> interface that says "bursty" while the kernel delivers uniform loss,
> which is the failure the feature exists to fix, wearing a disguise.

**`loss_burst_note`** `string` _(omitted when empty)_

### Client

Client is the joined view the UI renders: one row per device.

The join is station-dump LEFT JOIN (leases, neigh). Presence comes from the
radio; the address is decoration and may legitimately be absent while a
client is associated but has not finished DHCP.

**`mac`** `string`

**`ip`** `string` _(omitted when empty)_

**`ipv6`** `[]string` _(omitted when empty)_
> IPv6 is every routable v6 address the client currently holds. Privacy
> extensions mean a device usually has more than one, and each needs its
> own filter or part of its traffic escapes conditioning entirely.

**`hostname`** `string` _(omitted when empty)_

**`label`** `string`

**`medium`** `string`
> Medium is "wifi" or "wired". It affects only what telemetry exists --
> conditioning is identical for both, because policy is keyed by MAC and
> filters match on IP, neither of which knows about the physical layer.

**`port`** `string` _(omitted when empty)_
> Port is the bridge port the client was last seen on, for display.

**`radio_on`** `*RadioOn` _(omitted when empty)_
> RadioOn describes the access point a WIRELESS client is associated to.
>
> It matters now that the box serves two bands at once: a client on 2.4GHz
> at 20MHz and one on 5GHz at 80MHz behave completely differently, and
> until this existed the interface said only "wifi" for both. Absent for a
> wired client, and for a wireless one whose radio could not be read.

**`steer_to`** `string` _(omitted when empty)_
> SteerTo is the radio this client could be ASKED to move to, or empty when
> there is nowhere to send it.
>
> Computed here rather than left to the interface, which would otherwise
> have to work out the box's radio topology from the device list to decide
> whether to offer a button -- and would get it wrong for a client whose
> own radio is the only one serving. Empty is the honest answer in three
> cases that look different and are not: a wired client, a wireless client
> not currently associated, and a box with one radio.

**`beacon_reports`** `[]BeaconReport` _(omitted when empty)_
> BeaconReports is what THIS CLIENT last reported hearing, per BSS, from an
> 802.11k beacon request -- strongest first. See #228.
>
> The client's own measurement, taken at the client, which is the whole
> point: every other signal number in this struct is measured at the access
> point and exists only for the radio the client is associated to. These are
> the only numbers the box has for a radio the client is NOT on, and so the
> only ones that can say why a steer to it was refused.
>
> Empty until somebody asks. A beacon request is a request, and a client is
> free to decline it or to answer "not available".

**`present`** `bool`
> Present means currently associated to the radio. A DHCP lease alone does
> NOT set this: leases outlive the clients that held them.

**`shapeable`** `bool`
> Shapeable is false when the client has no IP yet, because every tc filter
> needs an address to match on.

**`station`** `*Station` _(omitted when empty)_

**`policy`** `Policy`

**`last_seen`** `int64`

**`last_active_ms`** `int64` _(omitted when empty)_
> LastActiveMs is the last time this client moved more than a trickle of
> traffic, in unix milliseconds. 0 means never seen doing anything.
>
> Distinct from LastSeen, which only says the device was THERE: a phone in a
> pocket is seen continuously and does nothing for hours. This is what
> orders a list of devices that are all equally idle right now, where
> "streaming ten seconds ago" and "silent since Tuesday" are the difference
> between the one worth looking at and the rest.

**`air_pct`** `float64` _(omitted when empty)_
> AirPct is how much of the radio this client cost over the last tick, as a
> percentage of wall clock -- transmit and receive together.
>
> The counterpart to DownCounters.ThroughputMbps and not a restatement of
> it: throughput says what crossed the link, this says what it took off the
> air, and the two come apart badly. Measured 2026-09-07 on wlan-usb, one
> client held 46% of the radio to move 34 Mbit/s while another held 77% to
> move 505.
>
> An OCCUPANCY, so a radio's clients do not sum to 100 and the remainder is
> not idle -- see Engine.airtimePct. Zero both for an idle client and for a
> radio whose driver cannot attribute airtime at all; only
> IfaceInfo.AirtimePerClient tells those apart, and anything drawing this
> must consult it first.

**`down_counters`** `Counters`
> DownCounters and UpCounters are the device default class. Sub-class
> counters are keyed by SubClass.ID.

**`up_counters`** `Counters`

**`sub_counters`** `map[string]Counters` _(omitted when empty)_

**`sweep`** `*SweepView` _(omitted when empty)_
> Sweep is the ladder sweep running on this device, or the outcome of the
> last one. Absent when the device has never been swept this daemon run.

**`rssi_run`** `*RssiView` _(omitted when empty)_
> RssiRun is what the distance model is imposing on this device, if one is
> set. Filled per tick and never persisted -- the stored side is
> Policy.Rssi, which holds only the operator's dBm.
>
> It exists so the controls can show what is ACTUALLY in force. Without it
> the sliders would read the stored policy, which under a model is
> untouched, and a device being handed 12 Mbit/s with corruption would show
> four sliders at zero.

**`pattern_run`** `*PatternView` _(omitted when empty)_
> PatternRun is the pattern playing on this device, if one is. Named apart
> from Policy.Pattern deliberately: that is the timeline as authored, this
> is a playhead moving along it, and a UI that confused the two would edit
> the wrong object.

### Counters

Counters is one class's enforcement statistics, converted to human units.

**`bytes`** `uint64`

**`packets`** `uint64`

**`drops`** `uint64`

**`overlimits`** `uint64`

**`backlog`** `uint64`

**`qlen`** `uint64`

**`throughput_mbps`** `float64`
> ThroughputMbps is derived between polls, not read from the kernel.

**`cap_mbps`** `float64`
> CapMbps is the rate the kernel is actually enforcing right now, read
> back from tc rather than echoed from the request, so the UI shows what
> the kernel believes rather than what we asked for.

### IfaceInfo

IfaceInfo is one interface as the bridge view draws it.

**`name`** `string`

**`role`** `string`

**`mac`** `string` _(omitted when empty)_

**`ipv4`** `[]string` _(omitted when empty)_

**`ipv6`** `[]string` _(omitted when empty)_

**`up`** `bool`
> Up is operstate == "up".

**`carrier`** `bool`
> Carrier is a THREE-state field flattened deliberately: sysfs `carrier`
> returns EINVAL on a down interface, so "no carrier" and "could not ask"
> are different facts. CarrierKnown says which this is; without it a
> perfectly healthy interface that happens to be down reports as a dead
> link. See Source J.

**`carrier_known`** `bool`

**`speed_mbps`** `int` _(omitted when empty)_
> SpeedMbps is meaningful only for a WIRED port. A bridge reports a speed
> too (br-lan reads 1000), which describes nothing when the bridge spans
> an 80MHz radio, so it is only populated for a real ethernet port.

**`master`** `string` _(omitted when empty)_
> Master is the bridge this interface is a port of, from the sysfs
> symlink. Cheaper than `bridge link show` and, unlike it, still answers
> for an interface that is down.

**`wireless`** `bool`

**`radio`** `*RadioInfo` _(omitted when empty)_

**`ap`** `*APStatus` _(omitted when empty)_

**`serving`** `bool`
> Serving marks a radio the daemon watches. Clients on any other radio are
> not conditioned and do not appear in the device list.

**`powered`** `bool`
> Powered is the rfkill state: false means the transmitter is switched off
> and the access point is silent without having told anyone. PowerKnown is
> false when the switch could not be read at all -- an unknown state must
> not render as "off", which would show a healthy radio as dead.

**`power_known`** `bool`

**`airtime_per_client`** `bool`
> AirtimePerClient says this radio's driver attributes airtime to
> individual stations, so the per-client airtime series means something on
> it. AirtimeCapKnown is false when no station has been on the radio to ask
> with -- the question is answered by observing a dump, not by driver name.
>
> Two flags rather than one for the same reason PowerKnown exists beside
> Powered: "cannot report" and "not asked yet" are different claims, and
> collapsing them would put a permanent warning on a radio that simply has
> nobody on it. Measured 2026-09-07 -- mt7921u reports it, the onboard
> brcmfmac does not, and its clients would otherwise draw as a radio full
> of idle devices.

**`airtime_cap_known`** `bool`

**`own_air_pct`** `float64` _(omitted when empty)_
> OwnAirPct is the share of this radio's time spent on OUR OWN clients,
> averaged over the last five seconds, summed across them.
>
> The trustworthy half of the pair, and the one that answers "how busy is
> this radio". It comes from our own station counters, which were verified
> against iperf3, and it is the same total the stacked chart draws. The
> neighbours' figure in AirView answers a different question -- how much of
> the CHANNEL everyone else is using -- and is only as good as the access
> points within earshot. Measured 2026-09-07 on a quiet channel 149 with
> both clients idle: this read 1.3% and the neighbours' figure read 32%.
>
> OwnAirKnown is false where the driver cannot attribute airtime, which is
> not 0% -- see AirtimePerClient.

**`own_air_known`** `bool` _(omitted when empty)_

### Keyframe

Keyframe is the whole conditioning policy at one instant on the timeline.

**`at_sec`** `float64`
> AtSec is the offset from the start of the pattern. Quantised to half a
> second by the API -- see validPattern for why the limit is there rather
> than in the millisecond range the kernel would accept.

**`down`** `Shape`

**`up`** `Shape`

**`ease`** `string` _(omitted when empty)_
> Ease governs how the run gets HERE from the preceding keyframe, so it is
> meaningless on the first one. Empty means EaseHold.

### Ladder

Ladder is one service's rendition ladder as seen by one device.

Keyed by service, not just by device. A ladder is a property of the content
and the player that fetched it, not of the hardware: the same set-top box
streaming two services produces two ladders with nothing in common, so a
device holds a list of these rather than one.

Service is typed by the operator. Inferring it from SNI or DNS would decay --
ECH removes SNI, QUIC buries the handshake, DoH removes the DNS -- and would
decay by silently mislabelling a ladder rather than by failing.

**`service`** `string`
> Service is the operator's name for what was being streamed, e.g.
> "netflix". Together with the device MAC it is the ladder's identity.

**`rungs`** `[]Rung`
> Rungs ascend.

**`provenance`** `string`
> Provenance is one of the Ladder* constants. It exists so the UI can show
> a measured ladder and a synthesised one with different confidence: they
> are different claims and must not render alike.

**`measured_at`** `int64` _(omitted when empty)_

**`note`** `string` _(omitted when empty)_

**`throttle`** `[]ThrottlePoint` _(omitted when empty)_
> Throttle is what the sweep incidentally learned about the SHAPER, at the
> caps where the client was pinned to it. It qualifies every rung above:
> rungs are a mean over a window, so they can be no more accurate or steady
> than the throttle that produced them.

### LinkEvent

LinkEvent is one entry on a pattern's link lane. A pulse (drop/nudge) fires
once as the playhead crosses AtSec; a deadzone re-fires for DurSec, holding
the client off long enough to drain a player's buffer.

**`at_sec`** `float64`

**`kind`** `string`

**`dur_sec`** `float64` _(omitted when empty)_

**`to_band_mhz`** `int` _(omitted when empty)_
> 	 * ToBandMHz is where a pin holds the client -- named as a BAND, not as an
> 	 * interface.
> 	 *
> 	 * A pattern is a stored, shareable artifact and interface names are box
> 	 * configuration: `wlan-usb` means nothing on a box whose adapter is called
> 	 * something else, and a pattern naming it would silently target the wrong
> 	 * radio or none. A band is a physical fact both boxes agree on, so the
> 	 * pattern says 2462 and the box decides which of its radios that is.
> 	 *
> 	 * It also matches what the model actually chooses. BestBandFor returns a
> 	 * frequency, so a walkabout emitting one is passing on its own decision
> 	 * rather than translating it into a name and back.
> 	 *
> 	 * Resolution can fail -- a box with no radio on that band has nowhere to
> 	 * pin the client -- and that is logged rather than swallowed. See
> 	 * Engine.fireLink.

**`scope`** `string` _(omitted when empty)_
> Scope is deadzone only, and empty means ScopeCurrent -- so a pattern
> saved before this field existed keeps doing exactly what it did.

### Match

Match narrows a sub-class to a subset of a device's traffic. An empty Match
is the device default and catches everything not claimed by a sub-class.

This is the "per-player on a shared IP" mechanism, with an important caveat:
it can only distinguish traffic that has a stable port or destination. A
phone's ephemeral source ports change per connection, so true per-player
separation requires a port-allocating proxy upstream (see README).

**`dst_port`** `int` _(omitted when empty)_
> DstPort matches the destination port for downlink traffic. 0 = any.

**`dst_cidr`** `string` _(omitted when empty)_
> DstCIDR matches the destination network, e.g. "23.32.0.0/16". Empty = any.

**`protocol`** `string` _(omitted when empty)_
> Protocol is "tcp", "udp", or "" for any.

### Notice

**`level`** `string`

**`text`** `string`

### Pattern

Pattern is an ordered list of keyframes plus how to leave the end of it.

There is deliberately no "wrap" mode. A loop restarts at the first keyframe,
so a pattern that should loop smoothly ends with a keyframe holding the same
values it began with -- which the operator can see on the timeline, unlike a
flag whose effect is invisible until playback. The UI offers to append that
closing keyframe; the runtime needs no concept for it.

**`name`** `string`

**`keys`** `[]Keyframe`

**`loop`** `bool`

**`links`** `[]LinkEvent` _(omitted when empty)_
> Links are Group A per-client link events on the same clock as the
> keyframes: association-level impairments (drop/nudge) and held outages
> (deadzone), as distinct from the rate/loss keyframes which condition
> packets. They fire as the playhead crosses them. See issue #135.

**`radios`** `[]RadioEvent` _(omitted when empty)_
> Radios are the adapter lanes: what happens to the box's own radios on
> this clock, as distinct from what happens to one client's packets or one
> client's association. A pattern carrying these is played against the BOX
> rather than a device -- see BoxBinding -- and a pattern may carry both,
> which is how "the radio dies mid-ladder-step" is expressible at all.

**`recipe`** `*PatternRecipe` _(omitted when empty)_
> Recipe records how a merged pattern was made, when it was made that way.
> Absent on anything hand-built, which stays entirely legal. See
> PatternRecipe.

### PatternRecipe

PatternRecipe is a merge expressed as its ingredients rather than its result.

# Why keep both

The keyframes are what plays, and they have to be: a pattern may be
hand-built, and one that is has no recipe to resolve. But a merge of
transient_shock and blackhole is 34 keyframes and 8.6 KB of absolute rates,
and that is a poor description of a thing whose actual identity is "those two
patterns, over this ladder". The recipe is 1.7 KB and says so.

# Why it carries no ladder

Because there is only one. The caps a pattern is built from are the sweep's
grid rather than the content's bitrates, and that grid is the same for every
device and every service on a box -- see GlobalLadder for the measurement
that establishes it.

So a recipe is just its ingredients: which patterns, in what order, at what
dwell. Naming a ladder would be recording a dependency that does not exist,
and it is the difference between a 1.7 KB object and a 200 byte one that a
person can read.

**`sources`** `[]string`
> Sources in the order they were selected, which IS the precedence rule:
> the first to drive an axis owns it. See MergePatterns.

**`dwell_sec`** `float64` _(omitted when empty)_
> DwellSec and Stretch as they were when the merge was made, so a rebuild
> reproduces it exactly rather than approximately.

**`stretch`** `float64` _(omitted when empty)_

### PatternView

PatternView is a run's progress, carried in every snapshot so the UI can draw
a moving playhead without polling a second endpoint.

Live only. A pattern DEFINITION is operator intent and lives on the policy; a
run is transient and a daemon restart cancels it.

**`state`** `string`
> State is "running", "paused" or "done".

**`name`** `string`

**`pos_sec`** `float64`
> PosSec is the playhead, and DurSec the position of the last keyframe.

**`dur_sec`** `float64`

**`loop`** `bool`

**`laps`** `int`
> Laps counts completed passes, so a long soak shows evidence of having
> looped rather than just a playhead that keeps resetting.

**`index`** `int`
> Index is the keyframe currently in force -- the one the run has passed
> most recently, not the one it is heading for.

**`down`** `Shape`
> Down and Up are what is being ENFORCED at this instant, which during a
> ramp is not equal to any keyframe.

**`up`** `Shape`

**`reason`** `string` _(omitted when empty)_

**`started_at`** `int64`

**`group`** `string` _(omitted when empty)_
> Group is the scenario this run belongs to, or empty for a lone run. The
> interface uses it to draw ONE transport for several devices rather than
> one per card, and to say which devices a stop is about to take with it.

### Policy

Policy is everything the operator has configured for one device. It is keyed
by MAC and persists across DHCP renewal, disconnection and reboot -- an IP is
too unstable to hang configuration from.

**`mac`** `string`

**`rev`** `uint64`
> Rev increments on every write to THIS device. A client sends the value it
> last saw as base_revision; a mismatch means someone else edited the same
> device in the meantime and the write is refused rather than silently
> clobbering them. Per-device rather than global, so two operators editing
> two different clients never collide.

**`label`** `string`

**`enabled`** `bool`

**`down`** `Shape`

**`up`** `Shape`

**`sub`** `[]SubClass`

**`ladders`** `[]Ladder` _(omitted when empty)_
> Ladders is every rendition ladder measured or entered for this device,
> one per service. Persisted with the policy because it is a durable input
> the operator owns and edits -- unlike telemetry, it is written once per
> sweep rather than once per tick, so it costs the SD card nothing.

**`pattern`** `*Pattern` _(omitted when empty)_
> Pattern is the timeline the operator has authored for this device, if
> any. Stored with the policy because it is intent, not telemetry: it is
> written when someone edits a keyframe, never once per tick. Storing it
> does not run it -- see Player.

**`rssi`** `*RssiModel` _(omitted when empty)_
> Rssi is the distance model driving this device, if one is. Nil means the
> operator is setting Down/Up by hand.
>
> The INPUT is stored and the derived shapes never are. Storing a model's
> output beside its input is how the two come to disagree -- the failure
> putLadder's provenance reset exists to prevent -- so the shapes are
> computed in desired() each tick and vanish the moment this is cleared.

### RadioEvent

RadioEvent is one entry on an adapter pattern's radio lanes.

Iface names the radio the event acts ON, and that is the whole grammar: an
off block takes that radio down, a deauth throws its clients off, an evict
sends them away from it, and a gather brings everyone else to it. One lane
per radio per kind, which is the link lanes' model one dimension wider.

**`at_sec`** `float64`

**`iface`** `string`

**`kind`** `string`

**`dur_sec`** `float64` _(omitted when empty)_
> DurSec is RadioOff and RadioAPDown only: how long the radio, or its
> access point, stays down. The other three are pulses and fire once as the
> playhead crosses them.

### RadioInfo

RadioInfo describes the interface serving the access point: whether it is
the onboard chip or a plugged-in USB adapter, and for USB, which speed the
link actually negotiated.

The speed is here because getting it wrong is invisible and expensive. A USB
3.0 Wi-Fi adapter that is not fully seated, or is on a cable without
SuperSpeed pins, enumerates as High-Speed and then behaves like a working
adapter in every respect that is normally checked -- same 80MHz channel, same
802.11ax, same PHY rate over 1 Gbit/s -- while delivering about a sixth of
the throughput. Measured here: 717 Mbit/s on USB 3.0 against 117 on USB 2.0,
same adapter, same radio settings, no error logged anywhere.

**`iface`** `string`

**`driver`** `string` _(omitted when empty)_

**`bus`** `string`
> Bus is "usb" or "onboard". Onboard is not a judgement: the Pi 5's chip is
> simply not on the USB bus, so none of the fields below apply to it.

**`product`** `string` _(omitted when empty)_
> Product and Vendor are the USB descriptor strings, so the interface can
> name the adapter rather than making the operator run lsusb.

**`vendor`** `string` _(omitted when empty)_

**`link_mbps`** `int` _(omitted when empty)_
> LinkMbps is the NEGOTIATED speed, from sysfs `speed`: 5000 for
> SuperSpeed, 480 for High-Speed. Not the advertised capability -- that is
> the whole point, since the two disagree exactly when it matters.

**`usb_version`** `string` _(omitted when empty)_
> USBVersion is bcdUSB as the device declares it, e.g. "3.20" or "2.10".

**`mac`** `string` _(omitted when empty)_
> MAC and Socket identify the PHYSICAL adapter behind this interface name.
>
> Carried with every reading because the name is not an identity. It is
> assigned by a udev rule, and on 2026-09-06 the rule matched any USB Wi-Fi
> adapter, so with two identical dongles "wlan-usb" meant one of them all
> evening and the other by morning -- silently, with every measurement in
> between attributed to a name rather than to hardware.
>
> The rule now names by socket, which is deterministic but deliberately
> makes the name follow the PORT: swap two dongles between sockets and both
> names stay put while the hardware behind them trades places. That is the
> right behaviour for configuring a box and the wrong thing to record
> against a measurement, so a measurement records all three.
>
> MAC is the adapter. Socket is the physical port, as the kernel path --
> "2-1", or "2-1.1" for something behind a hub. Iface is the friendly name
> an operator reads. Any two of them can disagree with the third, and when
> they do, that disagreement is the finding.

**`socket`** `string` _(omitted when empty)_

### RadioOn

Notice is one message for the operator.

	"error" - something is wrong and the box is not doing its job
	"info"  - a standing truth about how the system behaves

RadioOn is the access point a client is associated to, as the device list
shows it. A trimmed APStatus: the client card wants what distinguishes one
radio from the other, not the whole BSS configuration.

**`iface`** `string`

**`channel`** `int` _(omitted when empty)_

**`width_mhz`** `int` _(omitted when empty)_

**`mode`** `string` _(omitted when empty)_

**`band`** `string` _(omitted when empty)_
> Band is "2.4GHz" or "5GHz", derived from the channel. The single most
> useful fact about which radio a client is on.

**`serving`** `bool`
> Serving is whether hostapd's BSS is actually ENABLED on this radio.
>
> Separate from the channel, and not derivable from it: a DISABLED BSS
> still answers STATUS with the channel it will use when it comes back, so
> a radio serving nobody looks identical here to one serving happily. That
> is exactly what let a radio stop serving without anything noticing.

### RssiModel

RssiModel is a modelled signal level, and the exponent used to render it as a
distance.

dBm rather than metres is what is stored, deliberately. Metres depend on the
path-loss exponent, which is a per-building guess: a policy stored in metres
would mean a different impairment in a different building, or after someone
changed the exponent. dBm replays identically anywhere, and N is carried only
so the interface can show the metres the operator was looking at.

**`dbm`** `float64`

**`n`** `float64` _(omitted when empty)_

**`rx_db`** `float64`
> RxDb is what this device's ANTENNA costs it, in both directions.
>
> Antenna gain is reciprocal: the same small antenna that transmits poorly
> also receives poorly, so this moves downlink and uplink together. It is
> why changing the device kind changes what the device hears as well as how
> well it is heard -- an earlier version had only the transmit half and so
> left downlink untouched, which is wrong.

**`tx_db`** `float64`
> TxDb is the ADDITIONAL loss on uplink only, from transmitting at lower
> power than the access point.
>
> Neither is omitempty: 0 is meaningful for both -- it describes a device
> as capable as the access point -- and an omitted field would be
> indistinguishable from someone asking for that.

**`freq_mhz`** `int` _(omitted when empty)_
> FreqMHz and WidthMHz are the radio to model, for a client that is not on
> one -- a device on the wired port.
>
> Ignored when the client IS on a radio: there the band is a fact to be
> read, not a choice to be made, and letting it be overridden would let the
> interface disagree with the hardware. Only ever consulted as the answer
> to "there is no radio here, so which one should this behave like".

**`width_mhz`** `int` _(omitted when empty)_

**`auto_band`** `bool` _(omitted when empty)_
> AutoBand lets the model choose the radio at each distance, rather than
> holding the one it was given. Only meaningful without a real radio.

### RssiView

RssiView is what a distance model is imposing right now: the level asked for,
the distance that implies on the band this client is actually on, and the
shapes derived from it.

The same relationship to RssiModel that PatternView has to Pattern -- one is
the intent, stored; the other is what is happening, computed each tick.

**`dbm`** `float64`

**`distance_m`** `float64`

**`down_dbm`** `float64`
> DownDbm and UpDbm are the levels each direction actually arrives at,
> after the device's antenna and transmit power are taken off the path.
> Both are shown, because both move when either control moves and the pair
> is the whole explanation of why the directions differ.

**`up_dbm`** `float64`

**`freq_mhz`** `int` _(omitted when empty)_
> The radio actually used, which under AutoBand is the model's own choice
> and has to be shown -- a control set to "auto" that does not say what it
> picked is not reporting, it is hiding.

**`width_mhz`** `int` _(omitted when empty)_

**`down`** `Shape`

**`up`** `Shape`

### Rung

Rung is one rendition's delivered bitrate.

**`mbps`** `float64`
> Mbps is what the rendition costs on the wire.

**`up_at_mbps`** `float64` _(omitted when empty)_
> UpAtMbps is the cap at which the client CLIMBED INTO this rendition, and
> DownAtMbps the cap at which it FELL OUT of it. Both are recorded because
> the cost alone cannot drive a pattern.
>
> A player does not select a rendition merely because its bitrate fits. It
> wants headroom: measured on an iPhone, it took a variant only when the
> cap was around 1.5 to 1.9 times that variant's cost, never less than 1.5.
> So capping AT a rung's own bitrate does not hold a player on it -- it
> drops below. The cap that produces a given rendition is the useful
> number, and it is not the rendition's bitrate.
>
> The two differ from each other as well, because ABR players use
> hysteresis on purpose so they do not oscillate at a boundary. A sweep
> that climbs can only observe the up-switch thresholds; one that descends
> can only observe the down-switch ones. They are different measurements of
> the same ladder rather than competing attempts at one measurement.
>
> Zero means not observed in this run's direction.

**`down_at_mbps`** `float64` _(omitted when empty)_

**`unstable`** `bool` _(omitted when empty)_
> Unstable marks a rung whose observation window drifted: its two halves
> disagreed, so its mean describes neither.
> Reported rather than dropped, so the operator can see which number to
> distrust instead of being handed a uniformly confident list.

### ScanChannel

ScanChannel is the per-channel summary the recommendation is made from.

**`channel`** `int`

**`freq_mhz`** `int`

**`aps`** `int`
> APs found on this channel, excluding our own.

**`strongest_dbm`** `float64` _(omitted when empty)_
> Strongest neighbour, dBm. Closer to zero is louder, so a channel with one
> very loud neighbour is worse than one with three faint ones.

**`recommended`** `bool` _(omitted when empty)_

**`covering`** `int` _(omitted when empty)_
> Covering is every AP whose occupied spectrum includes this channel,
> including those merely primary on it. APs counts only the ones PRIMARY
> here, and the two are different facts: an 80MHz neighbour centred on 42
> covers 36/40/44/48 while being primary on one of them, so a channel with
> APs=0 and Covering=4 is fully occupied and looks empty by headcount.

**`util_pct`** `float64` _(omitted when empty)_
> UtilPct is measured airtime utilisation, 0-100, the HIGHEST reported by
> any AP on this channel. Utilisation is a property of the channel rather
> than of one BSS, so neighbours on the same channel broadly agree; taking
> the highest keeps a busy reading from being averaged away by an AP that
> heard less of it.

**`util_from`** `int` _(omitted when empty)_
> UtilFrom is how many APs on this channel reported BSS Load. Zero means
> nothing measured it and UtilPct must not be read -- a channel nobody
> advertised is not an idle one, and treating it as 0% would paint the
> busiest channel green.

**`util_min_pct`** `float64` _(omitted when empty)_
> UtilMinPct is the LOWEST reading on this channel, where UtilPct is the
> highest. Both, because the neighbours disagree by 11-14 points here and
> the gap between them is how much of a guess the number is.

**`stations`** `int` _(omitted when empty)_
> Stations is the total client count the BSS Load elements reported.

### ScanSummary

ScanSummary is what a scan concluded, small enough to carry in every poll.

At is why this is not just the channel list: a colour is only as good as its
timestamp, and a scan from an hour ago describes an hour-old room.

**`at`** `int64`

**`band`** `string` _(omitted when empty)_

**`channels`** `[]ScanChannel` _(omitted when empty)_

**`best_channel`** `int` _(omitted when empty)_

**`ours`** `map[string]float64` _(omitted when empty)_
> Ours is each of our OTHER radios as this scan heard it, keyed by
> interface, in dBm. The scanning radio is never in its own map.

**`looked`** `[]int` _(omitted when empty)_
> Looked is every channel this scan actually LISTENED to, as against the
> channels it found something on.
>
> The difference is load-bearing and cost a wrong reading to find. Channels
> only appear in Channels when an access point was heard there, so a
> genuinely clear channel produces no entry at all -- and a merge that
> asks "does this scan know about channel 149" by looking in Channels
> concludes "no" and falls back to an older scan that happened to hear
> something. MEASURED 2026-09-07: wlan-usb2 was saturated at 87% airtime on
> a clear channel 149 while the interface showed 2%, from a scan three
> minutes old, because every fresh scan heard nothing there and was judged
> to have said nothing.
>
> Hearing nothing on a channel you listened to is a measurement. This is
> what records that it was taken.

### ServiceInfo

ServiceInfo is one controllable service as the interface draws it.

**`name`** `string`

**`running`** `bool`
> Running is what systemd says now. False also covers a unit that failed
> or was never installed -- the button reads "start" in every one of those
> cases, which is the right offer.

### Shape

Shape is one direction's worth of conditioning. Zero values mean "no
conditioning of this kind", which is why RateMbps == 0 reads as unlimited
rather than as a total block.

**`rate_mbps`** `float64`
> RateMbps caps throughput. 0 means unlimited.

**`delay_ms`** `float64`
> DelayMs is added latency in ONE direction. A round trip crosses both
> directions, so the RTT a client observes is roughly down+up.

**`jitter_ms`** `float64`
> JitterMs randomises DelayMs by +/- this amount.

**`loss_pct`** `float64`
> LossPct is packet loss, 0-100. With LossBurst above 1 it is the MEAN loss
> rate rather than an independent per-packet probability -- the long-run
> fraction of packets lost, not the chance of losing any given one.

**`loss_burst`** `float64` _(omitted when empty)_
> LossBurst is the mean length of a loss burst, in packets. 1 (and 0, for
> stored policy written before this existed) means uniform loss: each
> packet independently, which is what netem does by default and what
> essentially never happens on a real link.
>
> Above 1 the kernel runs a Gilbert-Elliott model instead, and LossPct
> becomes the mean over time. The two knobs are chosen so that 1
> reproduces the old behaviour exactly, which is what lets every stored
> policy and keyframe keep meaning what it meant.
>
> Packets rather than milliseconds because netem's state machine steps per
> packet: packets is always defined, including at an unlimited rate where
> a duration is unknowable. The interface derives and shows the wall-clock
> equivalent from the configured rate.

**`reorder_pct`** `float64`
> ReorderPct is the share of packets released immediately instead of
> waiting in the delay queue, arriving ahead of packets sent before them.
>
> It REQUIRES DelayMs > 0. Reordering is implemented by letting a packet
> skip the delay queue, so with no queue there is nothing to skip -- and
> netem does not ignore the combination, it rejects the whole command:
> "reordering not possible without specifying some delay". A rejected
> command means NO netem qdisc, so an invalid reorder value would silently
> take the device's rate and loss down with it. Verified on 6.12.

**`corrupt_pct`** `float64`
> CorruptPct is the share of packets given a single-bit error, which fails
> the checksum and is discarded by the receiver rather than delivered
> damaged. Distinct from loss: the packet is transmitted and consumes the
> link, and TCP recovers by a different path than it does from a drop.

### Station

Station is the radio's view of a client: the authority on who is actually
associated. Byte counters here are from the ACCESS POINT's perspective, so
the AP's tx is the client's download.

**`mac`** `string`

**`signal_dbm`** `int`

**`tx_bytes`** `uint64`

**`rx_bytes`** `uint64`

**`tx_phy_mbps`** `float64`

**`rx_phy_mbps`** `float64`

**`tx_failed`** `uint64`
> TxFailed is transmit failures to this station. The Pi's Broadcom driver
> does not report per-station RSSI in AP mode -- there is no "signal" line
> in `iw station dump` at all -- so this is the available proxy for link
> quality. Rising quickly means the client is struggling.

**`connected_sec`** `int`

**`inactive_ms`** `int`

**`tx_duration_us`** `uint64` _(omitted when empty)_
> TxDurationUs and RxDurationUs are cumulative AIRTIME for this station in
> microseconds -- how long the radio actually spent transmitting to it and
> receiving from it, including the per-frame overhead that bytes do not
> see.
>
> Not derivable from the byte counters, and the gap is large. Measured
> 2026-09-07 on wlan-usb: a saturated client moved 1.31 GB in 13.40s of
> airtime, an effective 784 Mbit/s, while `tx bitrate` read 1200.9 -- the
> difference is preamble, IFS and ACK, which a full A-MPDU amortises. A
> second client trickling 34 Mbit/s came out at 129 Mbit/s effective from
> the same counters, because small unaggregated frames amortise nothing.
> So a device can be quiet on a throughput chart and expensive on the air,
> and only this pair tells them apart.
>
> Verified against an independent instrument rather than assumed: over two
> iperf3 runs `tx bytes` matched iperf3's own byte count to 4.5% (protocol
> overhead) and the effective rate these imply came out at 783.8 and 789.3
> Mbit/s -- 0.7% apart. See docs/DATA-CONTRACT.md Source T.

**`rx_duration_us`** `uint64` _(omitted when empty)_

**`duration_known`** `bool` _(omitted when empty)_
> DurationKnown says the dump actually CARRIED those lines.
>
> The distinction is the whole contract, because zero is a legitimate
> reading for an idle station and also what a driver that cannot answer
> leaves behind. Measured 2026-09-07: mt7921u reports both lines, and the
> Pi's onboard brcmfmac omits them entirely -- the same driver, and the
> same silence, as the missing per-station `signal` noted on TxFailed
> above. Reporting its clients at 0% would draw a busy radio as idle.

### SubClass

SubClass is a named rule inside a device, evaluated before the device
default. Order matters: sub-classes are installed at a higher filter priority
so the most specific rule wins.

**`id`** `string`

**`name`** `string`

**`match`** `Match`

**`down`** `Shape`

**`up`** `Shape`

**`enabled`** `bool`

### SurveyChannel

SurveyChannel is one channel's airtime, as a fraction of the time the radio
has actually spent on it.

**`freq_mhz`** `int`
> FreqMHz is the OPERATING frequency, taken from `iw dev <if> info` rather
> than from the survey block's own label. See ReportedFreqMHz.

**`reported_freq_mhz`** `int` _(omitted when empty)_
> ReportedFreqMHz is the frequency `iw survey dump` attached to this block,
> kept only so a disagreement is visible instead of silently resolved.
>
> Measured on mt7921u 2026-09-03: the AP was on 5200 MHz and the one
> populated block was labelled 5955 MHz -- a 6GHz channel the radio has
> never tuned to. iw maps the driver's survey INDEX onto its own channel
> enumeration and on this driver they do not line up. The airtime is real
> (it matched the AP's uptime to within 21 seconds); only the label is
> wrong. See Source L.

**`active_ms`** `int64`
> Totals are monotonic since the interface came up, in ms.

**`busy_ms`** `int64`

**`receive_ms`** `int64`

**`transmit_ms`** `int64`

**`busy_pct`** `*float64` _(omitted when empty)_
> BusyPct is derived from the DELTA since the previous call, not from the
> totals, so it describes the last few seconds rather than an average over
> the whole uptime of the access point. Absent on the first call, when
> there is no previous sample to difference against.

**`delta_active_ms`** `int64` _(omitted when empty)_

### SweepLevel

SweepLevel is the outcome of one cap level, kept so the operator can see how
each rung was arrived at rather than being handed a bare list of numbers.

**`level`** `int`

**`cap_mbps`** `float64`
> CapMbps is the cap held during this level. 0 is the opening
> unconditioned probe that establishes the ceiling.

**`rate_mbps`** `float64`
> RateMbps is the observed plateau: the MEAN over the window, which is the
> delivered rendition. See plateau() for why a mean and not a median.

**`drift`** `float64`
> Drift is how far the window's two halves disagreed, over its mean: how
> steady the rate was, which burstiness does not disturb.

**`saturated`** `bool`
> Saturated means throughput stayed up against the cap, so the client did
> not reveal a rung at this level.

**`new_rung`** `bool`
> NewRung means this level's plateau was accepted as a rung not already
> known.

**`samples`** `int`

**`suspect_skip`** `bool` _(omitted when empty)_
> SuspectSkip marks a jump wide enough that a rendition may sit inside it.
> A hint only: natural ladder spacing and single-skip gaps overlap.

### SweepView

SweepView is a ladder sweep's progress, carried in every snapshot so the UI
can show a run that takes minutes without polling a second endpoint.

Live only: a sweep is transient, and a daemon restart cancels it. What
survives a restart is the Ladder it produced, on the device's policy.

**`state`** `string`
> State is "running", "done" or "failed".

**`phase`** `string`
> Phase is "settling" while a new cap beds in, "observing" while the
> plateau is being measured, and mirrors State once the run has ended.

**`pass`** `string`
> Pass is "map" while finding where the rungs are, "measure" while
> measuring what they are. The two want opposite window lengths, which is
> why they are separate passes rather than one compromise.

**`service`** `string`

**`level`** `int`

**`cap_mbps`** `float64`
> CapMbps is the cap being held right now. 0 is the opening unconditioned
> probe that establishes the ceiling.

**`ceiling_mbps`** `float64`

**`found`** `[]Rung` _(omitted when empty)_
> Found is the rungs discovered so far, ascending. It grows during the run.

**`levels`** `[]SweepLevel` _(omitted when empty)_
> Levels is every level's outcome, so a surprising ladder can be read back
> to the observation that produced it rather than being taken on trust.

**`reason`** `string` _(omitted when empty)_

**`started_at`** `int64`

### ThrottlePoint

ThrottlePoint is a measurement of the conditioner itself, taken at a cap the
client could not live under.

A starved client is a perfect instrument. It fetches back to back, so the
delivered rate is decided entirely by the shaper -- the player has no say,
the content has no say, VBR has no say. Every sweep passes through at least
one such level on its way up and would otherwise discard the reading.

**`cap_mbps`** `float64`

**`delivered_mbps`** `float64`

**`ratio`** `float64`
> Ratio is delivered over configured. Framing overhead puts a real link
> slightly under 1.

**`variation`** `float64`
> Variation is the coefficient of variation across the window: the jitter a
> rung measured at this rate inherits from the shaper.

