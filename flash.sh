#!/usr/bin/env bash
#
# Writes a built image to an SD card on macOS.
#
# `dd` to the wrong device destroys a disk in seconds with no confirmation and
# no undo, so the guards here are deliberately hard to talk past.
set -euo pipefail

log() { printf '\033[36m==>\033[0m %s\n' "$*"; }
die() { printf '\033[31merror:\033[0m %s\n' "$*" >&2; exit 1; }

[ "$(uname)" = "Darwin" ] \
  || die "this helper is macOS-only; on Linux use dd or your usual card writer"

IMG="${1:-}"
if [ -z "$IMG" ]; then
  # Default to the newest build, so the common case is just ./flash.sh
  IMG=$(ls -t dist/*.img 2>/dev/null | head -1) || true
  [ -n "$IMG" ] || die "no image in dist/ -- run ./build.sh first"
fi
[ -f "$IMG" ] || die "no such image: $IMG"

# Enumerate every whole physical disk and mark which are eligible.
#
# Listing only "external" disks was wrong: a card sitting in a Mac's BUILT-IN
# SD reader reports Device Location: Internal, so the one disk you almost
# certainly want was the one disk that never appeared.
#
# "Removable Media: Removable" is the correct test. It catches built-in and USB
# card readers alike, while still excluding an internal system SSD and an
# external data drive, both of which report Fixed.
echo
log "Disks:"
printf '    %-9s %-10s %-32s %s\n' IDENTIFIER SIZE NAME ELIGIBLE
for d in $(diskutil list | awk '/^\/dev\/disk[0-9]+ \(/{gsub("/dev/","",$1); print $1}'); do
  I=$(diskutil info "/dev/$d" 2>/dev/null) || continue
  grep -q "Virtual: *Yes" <<<"$I" && continue   # disk images are not hardware
  grep -q "Whole: *Yes"   <<<"$I" || continue
  NM=$(awk -F': +' '/Device \/ Media Name/{print $2; exit}' <<<"$I")
  SZ=$(awk -F'[()]' '/Disk Size/{print $1; exit}' <<<"$I" | awk -F': +' '{print $2}')
  if grep -q "Removable Media: *Removable" <<<"$I"; then
    printf '    \033[32m%-9s\033[0m %-10s %-32s %s\n' "$d" "$SZ" "${NM:0:32}" "yes"
  else
    printf '    %-9s %-10s %-32s %s\n' "$d" "$SZ" "${NM:0:32}" "no (fixed disk)"
  fi
done
echo

DISK="${2:-}"
if [ -z "$DISK" ]; then
  read -r -p "Target disk (e.g. disk22, or /dev/disk22): " DISK
fi
DISK="${DISK#/dev/}"
[[ "$DISK" =~ ^disk[0-9]+$ ]] \
  || die "expected a whole-disk identifier like disk22, got '$DISK'"

INFO=$(diskutil info "/dev/$DISK" 2>/dev/null) || die "/dev/$DISK not found"

grep -q "Whole: *Yes" <<<"$INFO" \
  || die "/dev/$DISK is a partition, not a whole disk"
# Catches both mounted disk images and synthesised APFS containers -- neither
# is a physical device that can be written to.
grep -q "Virtual: *Yes" <<<"$INFO" \
  && die "/dev/$DISK is a virtual device, not physical hardware. Refusing."

# The disk holding the running system is never a valid target, whatever else it
# reports. Both the synthesised APFS container and its physical store are
# guarded, since either identifier could be typed.
BOOTDISK=$(diskutil info / 2>/dev/null | awk -F': +' '/Part of Whole/{print $2; exit}')
APFSPHYS=$(diskutil info / 2>/dev/null | awk -F': +' '/APFS Physical Store/{print $2; exit}')
# Strip only a trailing partition suffix: disk0s2 -> disk0. A ${var%%s*} here
# would cut at the "s" inside "disk" and yield "di", guarding nothing real
# while leaving the actual system disk wide open.
APFSWHOLE=$(sed -E 's/s[0-9]+$//' <<<"$APFSPHYS")
for guard in "$BOOTDISK" "$APFSWHOLE"; do
  if [ -n "$guard" ] && [ "$DISK" = "$guard" ]; then
    die "/dev/$DISK holds the running system. Absolutely not."
  fi
done

if ! grep -q "Removable Media: *Removable" <<<"$INFO"; then
  cat >&2 <<WARN

  /dev/$DISK reports its media as Fixed rather than Removable, so it looks like
  a hard disk rather than a card. Refusing by default.

  If you are certain this is your card reader, re-run with:
      BOA_ALLOW_FIXED=1 ./flash.sh "$IMG" $DISK

WARN
  [ "${BOA_ALLOW_FIXED:-}" = "1" ] || die "refusing a non-removable disk"
  log "BOA_ALLOW_FIXED is set -- proceeding against a fixed disk"
fi

SIZE=$(awk -F'[()]' '/Disk Size/{print $1; exit}' <<<"$INFO" | awk -F': +' '{print $2}')
NAME=$(awk -F': +' '/Device \/ Media Name/{print $2; exit}' <<<"$INFO")
BYTES=$(awk -F'[()]' '/Disk Size/{print $2; exit}' <<<"$INFO" | awk '{print $1}')
IMGBYTES=$(stat -f %z "$IMG")

# A card smaller than the image produces a truncated, unbootable write that
# only reveals itself when the Pi fails to start.
if [ -n "$BYTES" ] && [ "$BYTES" -lt "$IMGBYTES" ] 2>/dev/null; then
  die "card is $SIZE but the image needs $(( IMGBYTES / 1000000 )) MB"
fi

cat <<BANNER

  Writing : $IMG ($(du -h "$IMG" | cut -f1))
  To      : /dev/$DISK  --  ${NAME:-unknown}  ${SIZE:+($SIZE)}

  Everything currently on this disk will be destroyed.

BANNER
read -r -p "Type ERASE to continue: " CONFIRM
[ "$CONFIRM" = "ERASE" ] || die "aborted"

log "Unmounting /dev/$DISK"
diskutil unmountDisk "/dev/$DISK"

# /dev/rdiskN is the raw character device: it bypasses the buffer cache and is
# roughly an order of magnitude faster than the buffered /dev/diskN.
#
# Progress, because this is a multi-minute write to a card the user has just
# been asked to confirm is the right one. "Press Ctrl-T" was the previous
# answer, which requires knowing that SIGINFO exists and leaves the operator
# with no idea whether a silent minute means progress or a stall.
#
# pv when it is available -- it is the better tool and gives a rate and ETA
# from a single stream. Otherwise the image is written in chunks and the
# percentage is computed from the chunk index, which needs no extra program
# and no signal handling.
CHUNK_MB=32
render_bar() { # $1 = bytes done, $2 = bytes total, $3 = start epoch
  local done=$1 total=$2 start=$3 pct width filled elapsed rate eta
  pct=$(( done * 100 / total ))
  width=32
  filled=$(( pct * width / 100 ))
  elapsed=$(( $(date +%s) - start ))
  [ "$elapsed" -lt 1 ] && elapsed=1
  rate=$(( done / elapsed / 1048576 ))
  [ "$rate" -lt 1 ] && rate=1
  eta=$(( (total - done) / (rate * 1048576) ))
  printf '\r  [%-*s] %3d%%  %4d/%d MB  %d MB/s  ETA %d:%02d' \
    "$width" "$(printf '%*s' "$filled" '' | tr ' ' '#')" \
    "$pct" "$(( done / 1048576 ))" "$(( total / 1048576 ))" \
    "$rate" "$(( eta / 60 ))" "$(( eta % 60 ))"
}

if command -v pv >/dev/null 2>&1; then
  log "Writing ${IMGBYTES} bytes"
  sudo sh -c "pv -s '$IMGBYTES' '$IMG' | dd of='/dev/r$DISK' bs=4m" \
    || die "write failed"
else
  log "Writing (no pv installed; using chunked dd for progress)"
  # skip/seek are counted in units of bs, so both walk the same block index.
  # The final chunk is short and dd simply writes what remains.
  total_blocks=$(( (IMGBYTES + 4194303) / 4194304 ))
  start=$(date +%s)
  b=0
  while [ "$b" -lt "$total_blocks" ]; do
    sudo dd if="$IMG" of="/dev/r$DISK" bs=4m count="$CHUNK_MB" \
      skip="$b" seek="$b" conv=notrunc 2>/dev/null \
      || die "write failed at block $b of $total_blocks"
    b=$(( b + CHUNK_MB ))
    done_bytes=$(( b * 4194304 ))
    [ "$done_bytes" -gt "$IMGBYTES" ] && done_bytes=$IMGBYTES
    render_bar "$done_bytes" "$IMGBYTES" "$start"
  done
  printf '\n'
fi

log "Flushing"
sync
diskutil eject "/dev/$DISK" || true

cat <<'DONE'

  Done. Put the card in the Pi and cable it up:

    WAN port (eth0)  ->  your existing network   (required: clients get their
                                                  addresses from your router)
    USB ethernet     ->  a wired device under test   (optional)
    Wi-Fi            ->  clients join the boa SSID

  Then open  http://infinite-streaming-boa.local/  from anywhere on your network.

DONE
