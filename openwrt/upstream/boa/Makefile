#
# Copyright (C) 2026 Jonathan Oliver
#
# This is free software, licensed under the MIT License.
#
# THE UPSTREAM SHAPE, for openwrt/packages -- not the one openwrt-package.sh
# uses. The difference is the whole point of it:
#
#   openwrt/package/boa   takes a boad built on the developer's machine and
#                         packages it. Fast, ours, and a binary blob as far as
#                         a distribution feed is concerned.
#   openwrt/upstream/boa  fetches a source tarball by hash and COMPILES it on
#                         the builder, which is what openwrt/packages requires
#                         and what lets their buildbots produce every
#                         architecture rather than the two we build ourselves.
#
# The daemon has no third-party Go dependencies -- daemon/go.mod carries no
# require block at all -- so there is nothing to vendor and no module download
# during the build. That is the part that usually makes a Go submission hard,
# and it does not apply here.
#
# The web interface is the part that does. It is built with Vite from npm, and
# a buildbot has neither Node nor network, so the release tarball ships
# daemon/web/dist already built and Go embeds it. See issue #359 for the
# argument that has to be won with a reviewer before this is submitted.
#
include $(TOPDIR)/rules.mk

PKG_NAME:=boa
PKG_VERSION:=0.4.0
PKG_RELEASE:=1

PKG_SOURCE:=$(PKG_NAME)-$(PKG_VERSION).tar.gz
PKG_SOURCE_URL:=https://github.com/jonathaneoliver/infinite-streaming-boa/releases/download/v$(PKG_VERSION)
PKG_HASH:=skip

PKG_MAINTAINER:=Jonathan Oliver
PKG_LICENSE:=MIT
PKG_LICENSE_FILES:=LICENSE

PKG_BUILD_DEPENDS:=golang/host
PKG_BUILD_PARALLEL:=1
PKG_BUILD_FLAGS:=no-mips16

GO_PKG:=github.com/jonathaneoliver/infinite-streaming-boa/daemon
GO_PKG_BUILD_PKG:=$(GO_PKG)
# The daemon reports its own version in the interface header and in
# `boactl version`, and a package that lies about which build it is makes a
# support question unanswerable.
GO_PKG_LDFLAGS_X:=main.version=$(PKG_VERSION)

include $(INCLUDE_DIR)/package.mk
include $(TOPDIR)/feeds/packages/lang/golang/golang-package.mk

define Package/boa
  SECTION:=net
  CATEGORY:=Network
  SUBMENU:=Routing and Redirection
  TITLE:=Per-device link conditioner for a bridged access point
  URL:=https://github.com/jonathaneoliver/infinite-streaming-boa
  DEPENDS:=$(GO_ARCH_DEPENDS) +tc-full +kmod-netem +kmod-ifb +kmod-sched-core \
	+kmod-nft-bridge +ip-full +ip-bridge +iw
endef

define Package/boa/description
  Conditions each client's traffic independently -- rate, latency, jitter
  and loss, per device and per direction -- on a transparent bridge, and
  drives link events (drop, deadzone, steer, gather) through hostapd. Web
  interface on :8080 (http) and :8443 (https).

  The device must already be a transparent bridge with the full wpad build;
  this package configures no network of its own. See boa-setup check.
endef

define Package/boa/conffiles
/etc/config/boa
endef

# iperf3 is deliberately NOT a dependency here, where it is one in our own
# feed's package. It is how a user measures the link the box is conditioning,
# which makes it a companion rather than a requirement, and a distribution
# feed should not pull a server onto a router that did not ask for one.
define Package/boa/install
	$(INSTALL_DIR) $(1)/usr/libexec/boa $(1)/usr/sbin $(1)/etc/init.d $(1)/etc/config $(1)/lib/upgrade/keep.d
	$(INSTALL_BIN) $(GO_PKG_BUILD_BIN_DIR)/daemon $(1)/usr/libexec/boa/boad
	$(INSTALL_BIN) $(PKG_BUILD_DIR)/openwrt/files/usr/sbin/boa-setup $(1)/usr/sbin/boa-setup
	$(INSTALL_BIN) $(PKG_BUILD_DIR)/openwrt/files/etc/init.d/boa $(1)/etc/init.d/boa
	$(INSTALL_CONF) $(PKG_BUILD_DIR)/openwrt/files/etc/config/boa $(1)/etc/config/boa
	# Policies, ladders and remembered channels live here; a sysupgrade keeps them.
	echo /etc/infinite-streaming-boa/ > $(1)/lib/upgrade/keep.d/boa
endef

$(eval $(call GoBinPackage,boa))
$(eval $(call BuildPackage,boa))
