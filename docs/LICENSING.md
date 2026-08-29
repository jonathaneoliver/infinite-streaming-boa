# Licensing

pifi is MIT licensed and is designed so that it *stays* cleanly redistributable.
This document records the deliberate choices that keep it that way, so a future
change does not quietly compromise the position.

## The one rule that matters

**Ship the build scripts, not the built image.**

`dist/*.img` is a derivative of Raspberry Pi OS, which is a Debian derivative
containing several hundred packages under GPL-2.0, GPL-3.0, LGPL and — for the
Broadcom/Cypress wireless firmware — a proprietary license that permits
redistribution only in conjunction with Raspberry Pi hardware. Publishing that
2.8 GB artifact would drag every one of those obligations onto this repository,
including the GPL's requirement to offer corresponding source for everything in
it.

`build.sh` sidesteps all of it. The repository contains only our own MIT code;
the base image is downloaded from `downloads.raspberrypi.com` on the user's own
machine at build time, under Raspberry Pi's terms, and never redistributed by
us. `dist/` and `cache/` are gitignored for exactly this reason.

If you ever *do* want to publish prebuilt images, that is a real project with
real obligations — written offer of source, license manifest, firmware terms —
and not something to do casually.

## Runtime dependencies: invoked, never linked

The conditioner drives the kernel through command-line tools:

| Tool | License | How pifi uses it |
|---|---|---|
| `tc` (iproute2) | GPL-2.0-or-later | subprocess |
| `ip` (iproute2) | GPL-2.0-or-later | subprocess |
| `iw` | ISC | subprocess |
| `rfkill` (util-linux) | GPL-2.0 | subprocess |
| NetworkManager | GPL-2.0-or-later | config file + subprocess |
| dnsmasq | GPL-2.0 or GPL-3.0 | started by NetworkManager; we read its lease file |

Running a GPL program as a separate process and reading its output is ordinary
use, not derivation — no copyleft obligation propagates to pifi. This is why the
daemon shells out to `tc` rather than linking a netlink library that carries a
copyleft license.

**Do not link GPL or LGPL code into the daemon binary.** If a netlink library
ever becomes necessary for performance, use a BSD/MIT/Apache one, or keep it in
a separate process communicating over a pipe.

## Build and application dependencies

All permissive, all compatible with MIT:

| Component | License |
|---|---|
| Go standard library / toolchain | BSD-3-Clause |
| Vue 3 | MIT |
| Vite | MIT |
| vue-router | MIT |
| @tanstack/vue-query | MIT |
| @vueuse/core | MIT |
| TypeScript | Apache-2.0 |
| Debian base container (build only) | mixed, not redistributed |
| Rancher Desktop / Docker / colima | Apache-2.0, build-time only |

Rancher Desktop is not a dependency of the project — it is one of several ways
to provide a Linux kernel on macOS, and it is never shipped. On Linux there is
no container runtime requirement at all.

## Rules for keeping this clean

1. **No web fonts, no icon fonts, no CDN assets.** The Pi has no internet on the
   client network by design, so everything must be vendored anyway — and vendored
   fonts are the most common way a permissive project acquires an unexpected
   license. Use the system font stack.
2. **Charts are hand-rolled SVG.** Avoids pulling a charting library and its
   transitive tree into an offline appliance.
3. **Check `npm license` output before adding any dependency.** Reject anything
   GPL, AGPL, SSPL, BUSL or "source available".
4. **No vendored Raspberry Pi OS content in git** — not the image, not extracted
   firmware, not `.deb` files.

## Alternative: Apache-2.0

MIT is the default here because it is short and universally understood. If you
would rather have an explicit patent grant and a contributor-patent-retaliation
clause — worth considering for anything touching networking standards —
Apache-2.0 is a drop-in swap and remains compatible with every dependency above.

## Visualisation palette

The traffic charts are hand-rolled inline SVG with no charting dependency —
partly for licence hygiene, partly because the Pi serves this page over a link
the operator may have just throttled to 1 Mbps.

The two series colours are not a taste choice and should not be "tidied" without
re-validating:

| Role | Hex | Why |
|---|---|---|
| Downlink | `#3987e5` | Categorical slot 1, dark step |
| Uplink | `#d95926` | Categorical slot 2, dark step |

Validated against the chart surface `#151b23`: lightness band OKLCH L within
0.48–0.67, chroma floor met, CVD separation ΔE 26.8 (protan) / 32.4 (tritan),
normal-vision ΔE 31.8, both above 3:1 contrast.

The original cyan `#38bdf8` / amber `#fbbf24` pair **failed** the lightness band
on a dark surface (L 0.754 and 0.837, well above the 0.67 ceiling) even though it
separated fine under colour-vision deficiency simulation. Warm/cool beats
cool/warm-ish here: direction is the most confusable property in a bidirectional
conditioner, so it is carried by hue *and* by position *and* by label, never by
hue alone.
