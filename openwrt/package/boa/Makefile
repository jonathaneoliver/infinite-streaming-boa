#
# boa: the per-device link conditioner, as an OpenWrt package.
#
# Built by scripts/openwrt-package.sh, which cross-compiles boad (with the web
# interface embedded) on the host and hands it in as BOA_BIN: the SDK has Go in
# no feed it ships and no Node at all, and the binary is static, so the SDK
# only packages and signs it. The init script and config are the same files
# scripts/openwrt-deploy.sh installs, from ../../files.
#

include $(TOPDIR)/rules.mk

PKG_NAME:=boa
PKG_VERSION:=$(if $(BOA_VERSION),$(BOA_VERSION),0.0.0)
PKG_RELEASE:=$(if $(BOA_RELEASE),$(BOA_RELEASE),1)

PKG_MAINTAINER:=Jonathan Oliver
PKG_LICENSE:=MIT
PKG_LICENSE_FILES:=LICENSE

include $(INCLUDE_DIR)/package.mk

BOA_FILES:=$(CURDIR)/../../files

define Package/boa
  SECTION:=net
  CATEGORY:=Network
  TITLE:=Per-device link conditioner for a bridged access point
  URL:=https://github.com/jonathaneoliver/infinite-streaming-boa
  # NO ARCHITECTURE GATE. This carried `@aarch64`, which was true of every
  # target that had shipped and stopped being true the moment an x86 SDK was
  # used: the package built, installed and ran on an OpenWrt x86-64 guest on
  # 2026-09-24, with `boad` serving and kmod-netem loading against a real
  # OpenWrt kernel. scripts/openwrt-package.sh derives boad's architecture from
  # the SDK, so the binary and the label agree wherever this is built.
  #
  # NO DRIVER DEPENDENCIES EITHER, and that is a gap rather than a decision --
  # see #368. A device's own NIC and radio drivers are in the image on the Pi
  # and the Cudy, and absent on x86, where boa installs cleanly onto a box that
  # cannot see its adapters.
  DEPENDS:=+tc-full +kmod-netem +kmod-ifb +kmod-sched-core \
	+kmod-nft-bridge +ip-full +ip-bridge +iw +iperf3
endef

define Package/boa/description
  Conditions each client's traffic independently -- rate, latency, jitter
  and loss, per device and per direction -- on a transparent bridge, and
  drives link events (drop, deadzone, steer, gather) through hostapd. Web
  interface on :8080 (http) and :8443 (https).
endef

define Package/boa/conffiles
/etc/config/boa
endef

define Build/Prepare
	mkdir -p $(PKG_BUILD_DIR)
	$(CP) $(BOA_BIN) $(PKG_BUILD_DIR)/boad
endef

define Build/Compile
endef

define Package/boa/install
	$(INSTALL_DIR) $(1)/usr/libexec/boa $(1)/usr/sbin $(1)/etc/init.d $(1)/etc/config $(1)/lib/upgrade/keep.d
	$(INSTALL_BIN) $(PKG_BUILD_DIR)/boad $(1)/usr/libexec/boa/boad
	$(INSTALL_BIN) $(BOA_FILES)/usr/sbin/boa-setup $(1)/usr/sbin/boa-setup
	$(INSTALL_BIN) $(BOA_FILES)/etc/init.d/boa $(1)/etc/init.d/boa
	$(INSTALL_CONF) $(BOA_FILES)/etc/config/boa $(1)/etc/config/boa
	# Policies, ladders and remembered channels live here; a sysupgrade keeps them.
	echo /etc/infinite-streaming-boa/ > $(1)/lib/upgrade/keep.d/boa
endef

$(eval $(call BuildPackage,boa))
