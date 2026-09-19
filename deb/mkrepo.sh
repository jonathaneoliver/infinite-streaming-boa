#!/usr/bin/env bash
#
# Build infinite-streaming-boa .debs for arm64 and amd64, and a signed apt
# repository holding them.
#
# Runs INSIDE a debian:bookworm-slim container, as root; scripts/deb-package.sh
# starts it. Mounts: /src = deb/ (read-only), /in/<arch>/boad = the
# cross-compiled daemon, /keys/private.asc = the archive signing key, /out =
# dist/, where deb/ and apt/ are written.
#
#   mkrepo.sh <version>        e.g. 0.4.0+202609190300
#
# The repository is a standard one -- dists/stable/main/binary-<arch>/Packages,
# a pool/, and a Release signed both inline (InRelease) and detached
# (Release.gpg) -- so a client needs nothing but a signed-by keyring.
set -euo pipefail

VER="$1"
ARCHES="arm64 amd64"
POOL=pool/main/i/infinite-streaming-boa

apt-get -qq update >/dev/null
apt-get -qq install -y --no-install-recommends apt-utils gnupg >/dev/null

export GNUPGHOME
GNUPGHOME="$(mktemp -d)"
gpg --batch --quiet --import /keys/private.asc
KEY="$(gpg --batch --list-secret-keys --with-colons | awk -F: '$1 == "fpr" { print $10; exit }')"

rm -rf /out/deb /out/apt
mkdir -p /out/deb "/out/apt/$POOL"

for arch in $ARCHES; do
	R="$(mktemp -d)"
	cp -a /src/files/. "$R/"
	install -D -m 0755 "/in/$arch/boad" "$R/usr/bin/boad"
	install -d "$R/DEBIAN"
	install -m 0755 /src/DEBIAN/postinst /src/DEBIAN/prerm /src/DEBIAN/postrm "$R/DEBIAN/"
	install -m 0644 /src/DEBIAN/conffiles "$R/DEBIAN/"
	size="$(du -sk --exclude=DEBIAN "$R" | cut -f1)"
	sed -e "s/@VERSION@/$VER/" -e "s/@ARCH@/$arch/" -e "s/@SIZE@/$size/" \
		/src/DEBIAN/control.in > "$R/DEBIAN/control"
	(cd "$R" && find . -type f ! -path './DEBIAN/*' -printf '%P\0' | sort -z | xargs -0 md5sum) \
		> "$R/DEBIAN/md5sums"
	deb="/out/deb/infinite-streaming-boa_${VER}_${arch}.deb"
	dpkg-deb --root-owner-group -Zxz --build "$R" "$deb" >/dev/null
	cp "$deb" "/out/apt/$POOL/"
	echo "built $(basename "$deb")"
done

cd /out/apt
for arch in $ARCHES; do
	d="dists/stable/main/binary-$arch"
	mkdir -p "$d"
	apt-ftparchive --arch "$arch" packages pool > "$d/Packages"
	gzip -9nk "$d/Packages"
done
apt-ftparchive \
	-o APT::FTPArchive::Release::Origin=infinite-streaming-boa \
	-o APT::FTPArchive::Release::Label=infinite-streaming-boa \
	-o APT::FTPArchive::Release::Suite=stable \
	-o APT::FTPArchive::Release::Codename=stable \
	-o APT::FTPArchive::Release::Components=main \
	-o "APT::FTPArchive::Release::Architectures=$ARCHES" \
	-o "APT::FTPArchive::Release::Description=infinite-streaming-boa $VER" \
	release dists/stable > dists/stable/Release
gpg --batch --yes --local-user "$KEY" --clearsign -o dists/stable/InRelease dists/stable/Release
gpg --batch --yes --local-user "$KEY" --armor --detach-sign -o dists/stable/Release.gpg dists/stable/Release
# Binary for /etc/apt/keyrings (signed-by), and armored for reading.
gpg --batch --export "$KEY" > boa-archive-keyring.gpg
gpg --batch --armor --export "$KEY" > boa-archive-keyring.asc
echo "signed dists/stable with $KEY"
