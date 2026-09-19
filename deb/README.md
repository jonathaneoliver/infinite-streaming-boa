# boa as a Debian package

`infinite-streaming-boa`, for **arm64** (Raspberry Pi OS 64-bit) and **amd64**,
from a signed apt repository on GitHub Pages.

```sh
sudo install -d -m 0755 /etc/apt/keyrings
curl -fsSL https://jonathaneoliver.github.io/infinite-streaming-boa/apt/boa-archive-keyring.gpg \
  | sudo tee /etc/apt/keyrings/infinite-streaming-boa.gpg >/dev/null
echo "deb [signed-by=/etc/apt/keyrings/infinite-streaming-boa.gpg] https://jonathaneoliver.github.io/infinite-streaming-boa/apt stable main" \
  | sudo tee /etc/apt/sources.list.d/infinite-streaming-boa.list
sudo apt update && sudo apt install infinite-streaming-boa
```

## What it installs, and what it does not

| Installs | Does not |
|---|---|
| `/usr/bin/boad` | build the `br-lan` bridge |
| `infinite-streaming-boa.service` | write hostapd configurations (the image's radio planner) |
| `infinite-streaming-boa-iperf3.service` (not `iperf3.service`, which Debian's own iperf3 ships) | rename adapters (the image's udev rules) |
| `/etc/default/infinite-streaming-boa` (a conffile) | change `config.txt` |
| Depends: `iproute2 nftables iw hostapd rfkill iperf3`; Recommends `avahi-daemon` | |

That split is deliberate: a package that rewired a machine's network on
install could take it off the network. So on install the service is
**enabled**, and **started only if the bridge in `BOA_BRIDGE` already exists**;
otherwise the install says what is missing, and the service starts at the next
boot after the bridge is built. Set the ports in
`/etc/default/infinite-streaming-boa` and `sudo systemctl restart
infinite-streaming-boa`.

`remove` stops the service -- which removes every qdisc boad installed -- and
keeps the config and `/var/lib/infinite-streaming-boa`. `purge` deletes both.

For a box that does everything, flash the [Pi image](../README.md#1-a-raspberry-pi-5-from-an-image).

## Building

```sh
./scripts/deb-package.sh     # dist/deb/*.deb and a signed repository in dist/apt/
```

boad is cross-compiled on the host for both architectures; `deb/mkrepo.sh`
builds the packages with `dpkg-deb` and the repository with `apt-ftparchive` in
a `debian:bookworm-slim` container, and signs `InRelease` and `Release.gpg`.
The archive key is made once in `cache/apt-keys/` (gitignored). CI
(`.github/workflows/packages.yml`) signs with the `BOA_APT_GPG_PRIVATE_KEY`
secret and publishes the repository to `/apt/` beside the OpenWrt feed, on
every `v*` tag.

## Verified (2026-09-19)

In clean `debian:bookworm` containers, from the signed repository:

- arm64 and amd64 install; apt pulls in `hostapd iw nftables iperf3 rfkill`;
  `boad -version` runs.
- Without the keyring apt refuses the repository (`NO_PUBKEY`).
- `remove` keeps `/etc/default/infinite-streaming-boa`; `purge` deletes the state.
- The maintainer scripts, with `systemctl` stubbed: install with a `br-lan`
  bridge enables and restarts both units; without one it enables them and
  starts nothing; `remove` stops and disables; `upgrade` leaves them to
  `postinst`.

Not yet run on a real Raspberry Pi OS install with systemd.
