#!/usr/bin/env bash
#
# Save and restore a box's whole configuration: every device's conditioning,
# sub-classes, ladders and pattern.
#
# The box is not the system of record. Reflashing replaces the entire image,
# and a measured ladder costs about an hour of a real device streaming real
# content -- so it is worth keeping one of these in the repository.
#
#   ./scripts/config.sh export > profiles/box.json
#   ./scripts/config.sh import profiles/box.json          # merge (default)
#   ./scripts/config.sh import profiles/box.json replace  # box matches the file
#   ./scripts/config.sh export boa@192.168.1.9 > out.json
#
# merge   upserts the devices in the file and leaves every other device alone.
# replace additionally DELETES devices the file does not mention.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

BOX_DEFAULT=boa@infinite-streaming-boa.local

log() { printf '\033[36m==>\033[0m %s\n' "$*" >&2; }
die() { printf '\033[31merror:\033[0m %s\n' "$*" >&2; exit 1; }

cmd=${1:-}
case "$cmd" in
export)
	box=${2:-$BOX_DEFAULT}
	# Straight to stdout so it can be redirected into a file or piped to jq.
	# Errors go to stderr above, so a failed export cannot be mistaken for an
	# empty configuration.
	ssh "$box" 'curl -sf localhost/api/config' ||
		die "export failed; is the daemon running on $box?"
	;;
import)
	file=${2:-}
	[ -n "$file" ] || die "usage: $0 import <file> [merge|replace] [user@host]"
	[ -f "$file" ] || die "no such file: $file"
	mode=${3:-merge}
	box=${4:-$BOX_DEFAULT}
	case "$mode" in
	merge | replace) ;;
	*) die "mode must be merge or replace, got: $mode" ;;
	esac

	# Fail before touching the box rather than halfway through it.
	command -v python3 >/dev/null &&
		python3 -c 'import json,sys; json.load(open(sys.argv[1]))' "$file" ||
		die "not valid JSON: $file"

	if [ "$mode" = replace ]; then
		# replace deletes devices, so it asks. Everything else here is
		# additive and does not.
		n=$(python3 -c 'import json,sys; print(len(json.load(open(sys.argv[1]))["devices"]))' "$file")
		log "replace: the box will be left with exactly the $n device(s) in $file"
		printf 'continue? [y/N] ' >&2
		read -r reply
		case "$reply" in y | Y | yes) ;; *) die "cancelled" ;; esac
	fi

	log "importing $file ($mode) to $box"
	ssh "$box" "curl -sf -X POST -H 'Content-Type: application/json' \
		--data-binary @- 'localhost/api/config?mode=$mode'" <"$file" ||
		die "import refused; the box was left unchanged"
	printf '\n'
	;;
*)
	die "usage: $0 export [user@host] | $0 import <file> [merge|replace] [user@host]"
	;;
esac
