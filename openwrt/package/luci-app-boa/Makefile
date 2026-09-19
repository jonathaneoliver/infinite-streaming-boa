#
# luci-app-boa: Services -> infinite-streaming-boa in LuCI, framing boa's own interface.
#
# A plain package rather than luci.mk, so the SDK needs no luci feed to build
# it. Architecture-independent: a view, a menu entry and an ACL.
#

include $(TOPDIR)/rules.mk

PKG_NAME:=luci-app-boa
PKG_VERSION:=$(if $(BOA_VERSION),$(BOA_VERSION),0.0.0)
PKG_RELEASE:=$(if $(BOA_RELEASE),$(BOA_RELEASE),1)

PKG_MAINTAINER:=Jonathan Oliver
PKG_LICENSE:=MIT

include $(INCLUDE_DIR)/package.mk

BOA_FILES:=$(CURDIR)/../../files

define Package/luci-app-boa
  SECTION:=luci
  CATEGORY:=LuCI
  SUBMENU:=3. Applications
  TITLE:=LuCI page for boa
  DEPENDS:=+luci-base +boa
  PKGARCH:=all
endef

define Package/luci-app-boa/description
  Adds Services -> infinite-streaming-boa to LuCI: boa's interface in a frame, over http or
  https to match how LuCI was reached.
endef

define Build/Prepare
	mkdir -p $(PKG_BUILD_DIR)
endef

define Build/Compile
endef

define Package/luci-app-boa/install
	$(INSTALL_DIR) $(1)/www/luci-static/resources/view/boa
	$(INSTALL_DATA) $(BOA_FILES)/www/luci-static/resources/view/boa/boa.js $(1)/www/luci-static/resources/view/boa/boa.js
	$(INSTALL_DIR) $(1)/usr/share/luci/menu.d
	$(INSTALL_DATA) $(BOA_FILES)/usr/share/luci/menu.d/luci-app-boa.json $(1)/usr/share/luci/menu.d/luci-app-boa.json
	$(INSTALL_DIR) $(1)/usr/share/rpcd/acl.d
	$(INSTALL_DATA) $(BOA_FILES)/usr/share/rpcd/acl.d/luci-app-boa.json $(1)/usr/share/rpcd/acl.d/luci-app-boa.json
endef

# LuCI caches its menu, and rpcd reads ACLs only when it starts.
define Package/luci-app-boa/postinst
#!/bin/sh
[ -n "$${IPKG_INSTROOT}" ] || {
	rm -rf /tmp/luci-indexcache* /tmp/luci-modulecache
	/etc/init.d/rpcd reload
}
exit 0
endef

define Package/luci-app-boa/postrm
#!/bin/sh
[ -n "$${IPKG_INSTROOT}" ] || rm -rf /tmp/luci-indexcache* /tmp/luci-modulecache
exit 0
endef

$(eval $(call BuildPackage,luci-app-boa))
