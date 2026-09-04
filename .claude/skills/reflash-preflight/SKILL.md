---
name: reflash-preflight
description: Run the checks that must pass BEFORE writing a new image to the SD card, then walk the reflash through to a restored box. Invoke whenever the user is about to run ./build.sh or ./flash.sh, says "reflash", "rebuild the image", "write a new card", or changes anything outside the daemon binary (network profiles, systemd units, packages, kernel settings) that only a reflash can apply. Owns the export-state / verify-env / capture-hand-changes / build / flash / rejoin / import chain.
---

# reflash-preflight

A reflash replaces the whole filesystem. `/var/lib/infinite-streaming-boa/`
goes with it, and that directory holds the only expensive thing on the box:
a measured ladder costs about an hour of a real device streaming real content.

This has already gone wrong once. On 2026-08-31 the pifi→boa rename reflashed
the box and orphaned the state directory; the ladder and three patterns
survived only because an export had been taken first.

**The rule: nothing gets flashed until the state is exported and verified.**

## When to use

- Before `./build.sh` or `./flash.sh`
- "reflash", "rebuild the image", "write a new card", "flash it"
- After changing anything a deploy cannot carry: `scripts/customize.sh`,
  `overlay/`, `packages.txt`, network profiles, systemd units, kernel settings
- After changing `.env` — the hostname, SSID, user and passwords are baked in

If the change is only to the daemon or the UI, this skill is the wrong tool.
`./scripts/deploy.sh` takes about ten seconds. Say so and stop.

## 1. Export the state, and check it is actually current

Ask the box what it holds, rather than trusting the newest file in `profiles/`:

```sh
ssh boa@infinite-streaming-boa.local 'curl -s --max-time 5 localhost/api/config' \
  > profiles/pre-reflash-$(date +%Y%m%d-%H%M).json
```

Then compare against what the box reports live:

```sh
python3 - <<'PY'
import json, glob, os
f = max(glob.glob('profiles/pre-reflash-*.json'), key=os.path.getmtime)
d = json.load(open(f))
l = d.get('ladder')
print(f)
print(' patterns:', [p.get('name') for p in d.get('patterns') or []])
print(' ladder  :', 'NONE' if not l else f"{l.get('service')} {len(l.get('rungs',[]))} rungs")
PY
```

Confirm with the user that the pattern list and rung count match what they
expect to survive. A missing ladder here is the one failure that cannot be
recovered afterwards — stop and ask rather than flashing past it.

Version 2 documents carry **no devices**; that is correct, not a truncated
export. Per-device policy is cheap to recreate, the ladder is not.

## 2. Verify .env against .env.example

Every key the build reads must be documented, and every documented key set:

```sh
comm -23 <(grep -oE '^[A-Z_][A-Z0-9_]*=' .env | tr -d '=' | sort -u) \
         <(grep -oE '^#?\s*[A-Z_][A-Z0-9_]*=' .env.example | sed 's/^#\s*//' | tr -d '=' | sort -u)
comm -13 <(grep -oE '^[A-Z_][A-Z0-9_]*=' .env | tr -d '=' | sort -u) \
         <(grep -oE '^[A-Z_][A-Z0-9_]*=' .env.example | tr -d '=' | sort -u)
```

Both empty is the pass. Anything in the first list is a key the next person
cannot reproduce; anything in the second is a key the build may want.

If `.env` is being changed as part of this reflash, note that a `.env.bak*`
copy carries `AP_PASSWORD` and `BOA_PASSWORD` in cleartext. Those are
gitignored, and must stay that way.

## 3. Capture anything changed on the box by hand

A hand-edit made over SSH is lost on reflash and silently absent afterwards.
Find changes made after first boot, not merely today — the image writes most
of `/etc` at build time, so a same-day mtime proves nothing:

```sh
BOOT=$(ssh boa@infinite-streaming-boa.local 'uptime -s')
ssh boa@infinite-streaming-boa.local "sudo find /etc /boot -type f -newermt '$BOOT' 2>/dev/null"
ssh boa@infinite-streaming-boa.local "sudo find /var/lib/dpkg/info -name '*.list' -newermt '$BOOT'"
```

Run these **with** sudo and confirm sudo actually worked. Without it, `find`
skips every directory it cannot read and exits 0, so a box full of hand-edits
reports exactly the same empty output as a clean one. If sudo prompts for a
password, the box is missing its `/etc/sudoers.d/010_boa-nopasswd` rule — fix
that first, or this check is worthless.

`/usr/local/bin/boad` is expected — that is `deploy.sh`. Anything else must be
added to `scripts/customize.sh` or `overlay/` **in this same branch** before
flashing, or it is lost. That is exactly how the box ended up with no
passwordless sudo rule.

## 4. Build, flash, rejoin, restore

```sh
./build.sh          # validates .env first; ~5 min cold
./flash.sh          # writes the newest image; macOS
```

After it boots:

1. Rejoin the AP — SSID and password come from `.env`, and both may have changed
2. Confirm the daemon: `curl -s http://infinite-streaming-boa.local/api/health`
3. Import the export from step 1 through the UI's **import config** button

Import is merge-mode: pattern libraries merge by name, so nothing already on
the box is destroyed.

## 5. Verify the restore before calling it done

```sh
ssh boa@infinite-streaming-boa.local 'curl -s localhost/api/config' | python3 -c "
import json,sys; d=json.load(sys.stdin); l=d.get('ladder')
print('patterns:', [p.get('name') for p in d.get('patterns') or []])
print('ladder  :', 'NONE' if not l else f\"{l.get('service')} {len(l.get('rungs',[]))} rungs\")
"
```

The pattern names and rung count must match step 1. Report the comparison
explicitly rather than saying "restored" — a silent partial restore looks
identical to a working box until someone runs a sweep.

## 6. Check what the new kernel changed underneath you

A reflash is the only moment the kernel and its wireless drivers move, so it is
the moment to re-test the things that are currently blocked on them. Otherwise
a limitation gets written down once and quietly outlives the reason for it.

```sh
ssh boa@infinite-streaming-boa.local '
  uname -r
  /usr/sbin/iw dev wlan-usb info | grep txpower
  /usr/sbin/iw phy phy2 info | grep -E "\[(149|36)\]"
'
```

**`txpower 3.00 dBm`** means the `mt7921` driver is still unpatched: transmit
power cannot be set, and the non-goal in `PRD.md` still holds. **Anything near
`26.00 dBm`** on 5GHz means the fix has landed — re-run the sweep in
`docs/DATA-CONTRACT.md` Source Q, and if the signal moves, attenuation has
become an impairment this box can offer and the PRD non-goal has stopped being
true. See issue #202, which carries the method and the traps.

The channel list is worth a glance for the same reason: the allowlist in
`apChannels` was verified against a specific regulatory domain on specific
hardware, and a kernel that changes what a radio may do makes that a claim
worth re-checking rather than an assumption to inherit.

More generally: **anything the project records as "the hardware cannot do this"
is a candidate for re-testing here**, and nowhere else. A limitation is a
measurement with a date on it, not a permanent property.
