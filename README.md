# pifi

A Raspberry Pi that sits invisibly in your network and conditions each client's
internet connection independently — rate, latency, jitter and packet loss, per
device, in each direction, adjustable live from a web interface.

It is a **transparent bridge**, not a router. Devices under test keep their
normal addresses on your normal network, discovery protocols keep working, and
the Pi never appears as a hop in `traceroute`. Nothing being tested can tell it
is there.

```
                    [ your existing router ]
                              │
                            eth0                       ← WAN port, conditioning
                       ┌──────────────┐                  is applied here
                       │     pifi     │
                       └──────────────┘
                         br-lan  (one layer-2 segment)
                          ╱          ╲
                      wlan0          lan0
                    Wi-Fi AP     USB ethernet
                        │              │
                  wireless clients   wired client
                         ╲            ╱
                    conditioned identically
```

## Build an image

Needs `curl`, `docker`, `go` and `npm`. On macOS the Docker engine can come from
Rancher Desktop, Docker Desktop, colima or OrbStack — it exists only to supply a
Linux kernel, since macOS can neither mount ext4 nor loop-mount a partition
table. On Linux no container runtime is needed at all.

```sh
cp .env.example .env      # set your SSID, passphrase and country
./build.sh                # ~5 min first time, then cached
./flash.sh                # writes the newest image to an SD card
```

Cable the Pi's `eth0` to your existing network, optionally plug a USB ethernet
adapter in for a wired device under test, and open **http://pifi.local/**.

The build bakes in everything the Pi needs. It never downloads anything on first
boot, so it works on an air-gapped bench.

## Configuration

Everything lives in `.env`; see `.env.example` for the full annotated list.

| Variable | Meaning |
|---|---|
| `AP_SSID`, `AP_PASSWORD` | The wireless network the Pi publishes |
| `AP_COUNTRY` | Regulatory domain. **The radio stays blocked until this is right** |
| `AP_BAND`, `AP_CHANNEL` | `bg` (2.4GHz) or `a` (5GHz); 5GHz AP mode is limited to channels 36/40/44/48 |
| `PIFI_WAN_PORT` | The port cabled to your existing network. Conditioning is applied here |
| `PIFI_RESCUE_IP` | A fixed address on the bridge so the box is reachable even with no upstream DHCP |
| `PIFI_USER`, `PIFI_PASSWORD`, `PIFI_SSH_PUBKEY` | Headless login |

## How the conditioning works

Both directions are shaped on a **true egress queue** — the last interface the
packet crosses before leaving pifi:

| Direction | Where | Filter matches |
|---|---|---|
| Downlink (internet → client) | egress of the **client's own port** | destination IP |
| Uplink (client → internet) | egress of the **WAN port** | source IP |

Downlink accuracy is the priority, since the main use is throttling streaming
video on its way to a player. Shaping on the client's own port makes the shaper
the last thing to touch the packet, so the inter-packet spacing the player
measures is exactly what was configured.

**netem enforces the rate, not HTB.** HTB is a token bucket: while idle it
accumulates credit and then releases a burst at line rate when traffic resumes —
exactly when a player starts a segment and measures throughput, systematically
inflating its bandwidth estimate. netem instead computes each packet's
serialisation time from its length, which is what a real slow link does. HTB is
kept only as a classifier and per-client byte counter.

Measured on real forwarded traffic: caps from 1.5 to 50 Mbps deliver within
−4.5 % of target, and that residual is the Ethernet/IP/TCP framing overhead a
real link of the same speed would also impose. A configured 200 ms one-way
delay measured 200.6 ms RTT.

Policies are keyed by **MAC**, not IP, so they survive a DHCP renewal, a reboot,
and a client roaming between the wireless and wired ports.

### Sub-classes

Each device can carry rules that condition part of its traffic differently —
"video from this CDN gets 1.5 Mbps and 200 ms, everything else stays clean."
Match on destination port, network, and/or protocol.

Note what this can and cannot do. A sub-class distinguishes traffic by *service*,
not by application: a phone's ephemeral source ports change per connection, so
there is nothing stable to bind a per-app policy to. If you need true per-player
separation on a single device, put a port-allocating proxy in the path and match
on the ports it hands out — the two compose.

## Things that will mislead you if nobody says them

- **Wi-Fi airtime is shared.** Conditioning is *additive on top of* a variable
  radio baseline. One client's traffic still affects another's achievable rate
  no matter what the sliders say. This is the fundamental difference from a
  wired lab, and it is why the UI states it on screen.
- **Delay is per direction.** 100 ms each way is ~200 ms of round trip. The UI
  shows the computed total next to the inputs.
- **`overlimits` is not an error.** It counts how often a class hit its ceiling
  — a healthy throttled client shows it climbing constantly.
- **PHY rate is not throughput.** The radio routinely negotiates 400+ Mbps on a
  link carrying 2 Mbps.
- **The queue is sized from rate × delay.** netem's default 1000-packet queue
  would silently drop half the traffic on a "50 Mbps, 500 ms, 0 % loss" profile.
  pifi computes the queue depth instead, so configured loss is the only loss.

## Layout

```
build.sh              orchestrates the build; validates .env
flash.sh              writes an image to an SD card (macOS)
scripts/customize.sh  all image surgery; runs in a privileged arm64 container
scripts/build-payload.sh  builds the UI and cross-compiles the daemon
daemon/               Go daemon; embeds the compiled UI, ships as one binary
ui/                   Vue 3 + TypeScript interface
docs/DATA-CONTRACT.md where every displayed number comes from and what it means
docs/LICENSING.md     what may be redistributed, and what may not
overlay/              staged files grafted into the image root
```

## Development

Three loops, fastest first. Pick the slowest one you actually need.

### 1. Interface only, no hardware — sub-second

```sh
./scripts/dev.sh
```

Starts the daemon in **demo mode** and Vite with hot module replacement in front
of it. Editing a `.vue` file updates the browser immediately. No Pi, no root, no
image.

Demo mode is not a mock server — it is the real daemon with synthetic clients,
so it serves the same types through the same JSON encoding and the same SSE
transport. A separate mock would drift from production the first time a field
changed; this cannot.

The synthetic fleet deliberately includes the states that are tedious to
reproduce on real hardware and therefore never get styled: a client associated
but without an address yet, and a client configured but currently absent.
Throughput responds to the sliders, so the controls feel live.

### 2. Interface against a real Pi — sub-second, real data

```sh
./scripts/dev.sh pifi.local
```

Same hot reload, but the API calls proxy to a running Pi. Note this is
read-write: moving a slider really does condition that device's traffic.

### 3. Full deploy to a Pi — about ten seconds

```sh
./scripts/deploy.sh                    # pifi@pifi.local
./scripts/deploy.sh pifi@192.168.1.9
```

Builds the interface, cross-compiles the daemon, copies one binary, restarts one
service, and prints the health endpoint. Use this for changes to the daemon
itself, or to confirm a UI change on the real device.

**Reflashing the SD card is only needed when something outside the binary
changes** — network profiles, systemd units, packages, kernel settings. Day-to-day
work never touches the card.

Set up a key first, since this runs often:

```sh
ssh-copy-id pifi@pifi.local
```

### Useful

```sh
cd ui && npm run typecheck              # vue-tsc, no build
cd daemon && go vet ./...               # daemon also compiles on macOS
ssh pifi@pifi.local 'journalctl -u pifid -f'
ssh pifi@pifi.local 'tc -s class show dev wlan0'   # what the kernel really has
```

## Licence

MIT — see `LICENSE`. Every dependency is permissive; `docs/LICENSING.md` records
the audit and the one rule that keeps it that way: **ship the build scripts, not
built images.** A built image is a derivative of Raspberry Pi OS and carries
several hundred packages' worth of obligations that this repository does not.
