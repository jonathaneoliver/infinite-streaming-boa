# Builder image: everything image-surgery related happens in here, because
# macOS can neither mount ext4 nor loop-mount a partition table.
FROM debian:trixie-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
      util-linux mount e2fsprogs dosfstools xz-utils openssl \
      uuid-runtime ca-certificates parted kmod \
 && rm -rf /var/lib/apt/lists/*

COPY scripts/customize.sh /usr/local/bin/customize.sh
RUN chmod +x /usr/local/bin/customize.sh
ENTRYPOINT ["/usr/local/bin/customize.sh"]
