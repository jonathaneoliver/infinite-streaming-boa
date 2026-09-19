#!/usr/bin/env bash
#
# Package boa and luci-app-boa, and write a signed index for them.
#
# Runs INSIDE the OpenWrt SDK container, as root; scripts/openwrt-package.sh
# starts it. Mounts: /src = openwrt/ (read-only), /in/boad = the cross-compiled
# daemon, /keys = the signing key pair, /out = where the repository is written.
#
#   mkpkg.sh <version>        e.g. 0.4.0-r202609181830
#
# Why not `make package/boa/compile`: the SDK then compiles every package boa
# depends on that it has sources for -- all of package/kernel/linux, for the
# kmod-* dependencies -- and without the feeds it cannot resolve tc-full,
# iperf3 or luci-base at all. The packages here compile nothing, so this does
# what include/package-pack.mk does for an apk build and nothing else: the same
# install scripts, the same /lib/apk/packages metadata, the same `apk mkpkg`
# fields, and an index signed the way package/Makefile signs one. Dependencies
# and descriptions are read from the Makefiles, which remain the definition --
# and which still build these packages in a buildroot that has the feeds.
set -euo pipefail

VER="$1"
APK=/builder/staging_dir/host/bin/apk
SRC=/src
OUT=/out
URL=https://github.com/jonathaneoliver/infinite-streaming-boa
# The target's package architecture, from the SDK's own staging dir:
# target-aarch64_cortex-a76_musl -> aarch64_cortex-a76.
ARCH="$(ls /builder/staging_dir | sed -n 's/^target-\(.*\)_musl$/\1/p' | head -1)"
[ -n "$ARCH" ] || { echo "cannot tell the SDK's package architecture" >&2; exit 1; }

# The runtime dependencies in a Makefile's DEPENDS, one "+pkg" per entry and
# continuation lines followed. "@cond" build conditions are not dependencies.
makefile_depends() {
	sed -n '/^  DEPENDS:=/,/[^\\]$/p' "$1" |
		sed 's/^  DEPENDS:=//; s/\\$//' | tr -s ' \t' '\n' |
		sed -n 's/^+//p' | tr '\n' ' ' | sed 's/ *$//'
}

makefile_description() { # makefile pkgname
	sed -n "/^define Package\/$2\/description/,/^endef/p" "$1" |
		sed '1d;$d' | tr -s ' \t\n' ' ' | sed 's/^ //; s/ $//'
}

# pack <name> <arch> <rootdir> [conffile...]
pack() {
	local name=$1 arch=$2 root=$3; shift 3
	local mk="$SRC/package/$name/Makefile"
	local ctl
	ctl="$(mktemp -d)"
	mkdir -p "$root/lib/apk/packages"

	# Install scripts, as include/package-pack.mk writes them. default_postinst
	# enables and starts the package's init scripts; default_prerm stops and
	# disables them.
	{
		echo '#!/bin/sh'
		echo '[ "${IPKG_NO_SCRIPT}" = "1" ] && exit 0'
		echo '[ -s "${IPKG_INSTROOT}/lib/functions.sh" ] || exit 0'
		echo '. ${IPKG_INSTROOT}/lib/functions.sh'
		echo 'export root="${IPKG_INSTROOT}"'
		echo "export pkgname=\"$name\""
		echo 'add_group_and_user'
		echo 'default_postinst'
		makefile_script "$mk" "$name" postinst
	} > "$ctl/post-install"
	{
		echo '#!/bin/sh'
		echo 'export PKG_UPGRADE=1'
		sed '/^\s*#!/d' "$ctl/post-install"
	} > "$ctl/post-upgrade"
	{
		echo '#!/bin/sh'
		echo '[ -s "${IPKG_INSTROOT}/lib/functions.sh" ] || exit 0'
		echo '. ${IPKG_INSTROOT}/lib/functions.sh'
		echo 'export root="${IPKG_INSTROOT}"'
		echo "export pkgname=\"$name\""
		echo 'default_prerm'
	} > "$ctl/pre-deinstall"
	local scripts=(
		--script "post-install:$ctl/post-install"
		--script "post-upgrade:$ctl/post-upgrade"
		--script "pre-deinstall:$ctl/pre-deinstall"
	)
	makefile_script "$mk" "$name" postrm > "$ctl/postrm"
	if [ -s "$ctl/postrm" ]; then
		sed -i '1i #!/bin/sh' "$ctl/postrm"
		scripts+=(--script "post-deinstall:$ctl/postrm")
	fi

	# The file list before the conffiles are added, as package-pack.mk does.
	(cd "$root" && find . -type f,l -printf '/%P\n' | sort) > "$ctl/list"
	mv "$ctl/list" "$root/lib/apk/packages/$name.list"
	if [ $# -gt 0 ]; then
		printf '%s\n' "$@" > "$root/lib/apk/packages/$name.conffiles"
		for f in "$@"; do
			echo "$f $(sha256sum "$root$f" | cut -d' ' -f1)"
		done > "$root/lib/apk/packages/$name.conffiles_static"
	fi

	"$APK" mkpkg \
		--info "name:$name" \
		--info "version:$VER" \
		--info "description:$(makefile_description "$mk" "$name")" \
		--info "arch:$arch" \
		--info "license:MIT" \
		--info "origin:boa-openwrt" \
		--info "url:$URL" \
		--info "maintainer:Jonathan Oliver" \
		"${scripts[@]}" \
		--info "depends:$(makefile_depends "$mk")" \
		--files "$root" \
		--output "$OUT/$name-$VER.apk"
	echo "packed $name-$VER ($arch): depends $(makefile_depends "$mk")"
}

# The body of a Makefile's `define Package/<name>/<script>`, minus its #! line.
makefile_script() { # makefile pkgname script
	sed -n "/^define Package\/$2\/$3$/,/^endef/p" "$1" |
		sed '1d;$d' | sed '/^\s*#!/d; s/\$\$/$/g'
}

F="$SRC/files"
rm -f "$OUT"/*.apk "$OUT"/packages.adb

# boa
R="$(mktemp -d)"
install -d "$R/usr/libexec/boa" "$R/etc/init.d" "$R/etc/config" "$R/lib/upgrade/keep.d"
install -m 0755 /in/boad "$R/usr/libexec/boa/boad"
install -m 0755 "$F/etc/init.d/boa" "$R/etc/init.d/boa"
install -m 0644 "$F/etc/config/boa" "$R/etc/config/boa"
echo /etc/infinite-streaming-boa/ > "$R/lib/upgrade/keep.d/boa"
pack boa "$ARCH" "$R" /etc/config/boa

# luci-app-boa
R="$(mktemp -d)"
install -d "$R/www/luci-static/resources/view/boa" "$R/usr/share/luci/menu.d" "$R/usr/share/rpcd/acl.d"
install -m 0644 "$F/www/luci-static/resources/view/boa/boa.js" "$R/www/luci-static/resources/view/boa/boa.js"
install -m 0644 "$F/usr/share/luci/menu.d/luci-app-boa.json" "$R/usr/share/luci/menu.d/luci-app-boa.json"
install -m 0644 "$F/usr/share/rpcd/acl.d/luci-app-boa.json" "$R/usr/share/rpcd/acl.d/luci-app-boa.json"
pack luci-app-boa noarch "$R"

# The index, signed as package/Makefile signs one. Packages are trusted
# through it: a device with the public key in /etc/apk/keys installs from this
# directory without --allow-untrusted.
cd "$OUT"
"$APK" mkndx --allow-untrusted --sign-key /keys/private-key.pem \
	--description "boa $VER" --output packages.adb ./*.apk
echo "indexed and signed: $(ls ./*.apk | tr '\n' ' ')"
