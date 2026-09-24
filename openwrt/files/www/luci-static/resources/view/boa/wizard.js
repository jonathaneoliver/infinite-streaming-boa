'use strict';
'require view';
'require uci';
'require ui';
'require rpc';
'require fs';

// Services -> boa setup: the first-run questions, asked once, in a browser.
//
// SEPARATE FROM THE BOA PAGE ON PURPOSE. Services -> infinite-streaming-boa
// frames boad's own interface, which is the conditioning dashboard and assumes
// a device that already serves clients. This page is what gets it there, so it
// is its own entry and touches nothing boad owns -- it writes `wireless` and
// the root password, and nothing in `boa`.
//
// OVER THE WIRED LAN, NECESSARILY. A device fresh from a flash ships both
// wifi-ifaces with `option disabled '1'` -- measured on a factory-reset Cudy
// TR3000, 2026-09-24: no wireless interfaces and no hostapd at all. There is no
// access point to join, so Wi-Fi cannot be configured over Wi-Fi, and this page
// is reached over the LAN port. Applying takes the radios down and back up, so
// a browser on Wi-Fi would cut itself off mid-apply. Said on the page, because
// the person reading it is the one who would do that.

var callSetPassword = rpc.declare({
	object: 'luci',
	method: 'setPassword',
	params: [ 'username', 'password' ]
});

var callBoardInfo = rpc.declare({
	object: 'system',
	method: 'board'
});

var callNetworkDevices = rpc.declare({
	object: 'luci-rpc',
	method: 'getNetworkDevices',
	expect: { '': {} }
});

// The board name and the last two octets of the LAN MAC: `cudy-3f16`.
//
// A MAC is broadcast in every beacon, so a NAME built from one gives nothing
// away. A passphrase built from one would give everything away, which is why
// only this is derived. The prefix comes from the board rather than being
// hardwired, because this package also runs on a Pi 5, an x86 container and
// OpenWrt on x86, where "cudy" would be a lie.
function defaultSSID(board, devices) {
	var model = (board && (board.model || board.board_name)) || '';
	var prefix = String(model).trim().split(/\s+/)[0].toLowerCase().replace(/[^a-z0-9]/g, '');
	if (!prefix)
		prefix = 'boa';

	var mac = '';
	var names = [ 'br-lan', 'eth0', 'eth1' ];
	for (var i = 0; i < names.length && !mac; i++)
		if (devices[names[i]] && devices[names[i]].mac)
			mac = devices[names[i]].mac;
	if (!mac)
		for (var k in devices)
			if (devices[k] && devices[k].mac && !/^(lo|wlan)/.test(k)) { mac = devices[k].mac; break; }

	var suffix = String(mac).replace(/:/g, '').toLowerCase().slice(-4);
	return suffix ? prefix + '-' + suffix : prefix;
}

function row(label, control, help) {
	return E('div', { 'class': 'cbi-value' }, [
		E('label', { 'class': 'cbi-value-title' }, [ label ]),
		E('div', { 'class': 'cbi-value-field' }, [
			control,
			help ? E('div', { 'class': 'cbi-value-description' }, [ help ]) : ''
		])
	]);
}

return view.extend({
	load: function() {
		return Promise.all([
			uci.load('wireless'),
			callBoardInfo().catch(function() { return {}; }),
			callNetworkDevices().catch(function() { return {}; }),
			uci.load('attendedsysupgrade').catch(function() { return null; })
		]);
	},

	render: function(data) {
		var board = data[1] || {}, devices = data[2] || {};
		var devs = uci.sections('wireless', 'wifi-device');
		var aps  = uci.sections('wireless', 'wifi-iface').filter(function(s) {
			return s.mode == 'ap' || s.mode == null;
		});

		if (!devs.length)
			return E('div', { 'class': 'cbi-map' }, [
				E('h2', {}, [ _('boa setup') ]),
				E('div', { 'class': 'alert-message warning' }, [
					_('No radios in /etc/config/wireless, so there is nothing to set up here.')
				])
			]);

		var curSSID = aps.length ? (aps[0].ssid || '') : '';
		if (!curSSID || curSSID == 'OpenWrt')
			curSSID = defaultSSID(board, devices);

		var ssid    = E('input', { 'type': 'text', 'class': 'cbi-input-text', 'value': curSSID, 'maxlength': 32 });
		var key     = E('input', { 'type': 'password', 'class': 'cbi-input-password' });
		var key2    = E('input', { 'type': 'password', 'class': 'cbi-input-password' });
		var country = E('input', { 'type': 'text', 'class': 'cbi-input-text', 'maxlength': 2,
		                           'value': (devs[0].country && devs[0].country != '00') ? devs[0].country : '',
		                           'style': 'text-transform:uppercase; width:5em' });
		var pw      = E('input', { 'type': 'password', 'class': 'cbi-input-password' });
		var pw2     = E('input', { 'type': 'password', 'class': 'cbi-input-password' });

		var status = E('div', {});

		var apply = E('button', { 'class': 'cbi-button cbi-button-apply important', 'click': function(ev) {
			ev.target.blur();
			status.innerHTML = '';

			var s = ssid.value.trim(), k = key.value, c = country.value.trim().toUpperCase();

			function bad(msg) {
				status.appendChild(E('div', { 'class': 'alert-message warning' }, [ msg ]));
				return false;
			}

			if (!s.length || s.length > 32)
				return bad(_('An SSID is 1 to 32 characters.'));
			if (k.length && (k.length < 8 || k.length > 63))
				return bad(_('A WPA passphrase is 8 to 63 characters. Leave it empty for an open network.'));
			if (k !== key2.value)
				return bad(_('The two passphrases do not match.'));
			// '00' is not a country: hostapd refuses to parse it as a
			// country_code and the access point never starts. An EMPTY value is
			// a different thing entirely -- it is the world domain, and radios
			// come up on it -- so the option is deleted rather than set to '00'.
			if (c == '00')
				return bad(_('"00" is not a country: hostapd rejects it as an invalid country_code. Leave it empty instead, which is the world domain and works.'));
			if (c.length && !/^[A-Z]{2}$/.test(c))
				return bad(_('A country code is two letters, or empty.'));
			if (pw.value !== pw2.value)
				return bad(_('The two root passwords do not match.'));

			aps.forEach(function(ap) {
				uci.set('wireless', ap['.name'], 'ssid', s);
				uci.set('wireless', ap['.name'], 'disabled', '0');
				// Not cosmetic: without these, measure is refused and steer
				// fails, which is most of what boa does to a radio.
				uci.set('wireless', ap['.name'], 'ieee80211k', '1');
				uci.set('wireless', ap['.name'], 'bss_transition', '1');
				if (k.length) {
					uci.set('wireless', ap['.name'], 'encryption', 'psk2');
					uci.set('wireless', ap['.name'], 'key', k);
				} else {
					uci.set('wireless', ap['.name'], 'encryption', 'none');
					uci.unset('wireless', ap['.name'], 'key');
				}
			});

			devs.forEach(function(d) {
				uci.unset('wireless', d['.name'], 'disabled');
				if (c.length)
					uci.set('wireless', d['.name'], 'country', c);
				else
					uci.unset('wireless', d['.name'], 'country');
			});

			// LuCI's firmware-upgrade dialog asks on EVERY Status -> Overview
			// load until answered once, because it is driven by the preference
			// being UNSET rather than by checking being enabled. Turned off
			// here, since answering yes has LuCI fetch .versions.json, a
			// profiles.json and the sysupgrade API from the downloads site
			// every time that page opens, and a bench appliance should not make
			// outbound calls nobody asked for. Only when UNSET: an operator who
			// turned checking on meant it.
			if (uci.get('attendedsysupgrade', 'client', 'login_check_for_upgrades') == null)
				uci.set('attendedsysupgrade', 'client', 'login_check_for_upgrades', '0');

			ui.showModal(_('Applying'), [ E('p', { 'class': 'spinning' }, [
				_('Writing the wireless configuration and restarting the radios.')
			]) ]);

			return uci.save()
				.then(function() { return uci.apply(); })
				.then(function() { return fs.exec('/sbin/wifi'); })
				.then(function() {
					if (!pw.value.length)
						return null;
					return callSetPassword('root', pw.value);
				})
				.then(function() {
					ui.hideModal();
					status.appendChild(E('div', { 'class': 'alert-message success' }, [
						E('p', {}, [ _('Applied. "%s" is being brought up on every radio.').format(s) ]),
						E('p', {}, [ pw.value.length
							? _('The root password is set. The next login will ask for it.')
							: _('The root password was left as it is.') ]),
						E('p', {}, [ _('Confirm it from a shell with: boa-setup check') ])
					]));
					pw.value = pw2.value = key.value = key2.value = '';
				})
				.catch(function(e) {
					ui.hideModal();
					// Loudly. A half-applied wizard leaves a device with an SSID
					// nobody chose, and the one thing worse than that is not
					// being told.
					status.appendChild(E('div', { 'class': 'alert-message danger' }, [
						E('p', {}, [ _('Did not finish: %s').format(e.message || e) ]),
						E('p', {}, [ _('The device may be partly configured. Check it with: boa-setup check') ])
					]));
				});
		} }, [ _('Apply') ]);

		return E('div', { 'class': 'cbi-map' }, [
			E('h2', {}, [ _('boa setup') ]),
			E('div', { 'class': 'cbi-map-descr' }, [
				_('The first-run settings a device needs before it can serve clients for boa to condition.'), ' ',
				_('Applying also turns on 802.11k and BSS transition on every access point, which boa needs for measure and steer, and switches off LuCI\'s check-for-firmware-upgrades popup unless you have already answered it.')
			]),
			E('div', { 'class': 'alert-message warning' }, [
				E('p', {}, [ _('Use this over the wired LAN port.') ]),
				E('p', {}, [ _('A device fresh from a flash has its radios disabled, so there is no network to join, and applying takes every radio down and back up. A browser connected over Wi-Fi would cut itself off part-way through.') ])
			]),

			E('div', { 'class': 'cbi-section' }, [
				E('h3', {}, [ _('Wi-Fi') ]),
				row(_('Network name (SSID)'), ssid,
					_('Suggested from the board name and the last two octets of the LAN MAC. A MAC is in every beacon, so a name built from one reveals nothing.')),
				row(_('Passphrase'), key,
					_('8 to 63 characters, or empty for an open network. Never derived from the MAC: anyone in radio range can read that.')),
				row(_('Passphrase again'), key2),
				row(_('Country'), country,
					_('Optional. It unlocks DFS channels, 2.4 GHz ch 12/13 and 3-6 dB of power. Left empty the radios still work, on the world domain at 20 dBm -- measured on both bands. A wrong country is a regulatory answer, so it is asked rather than guessed.'))
			]),

			E('div', { 'class': 'cbi-section' }, [
				E('h3', {}, [ _('Access') ]),
				E('div', { 'class': 'cbi-value-description' }, [
					_('A device fresh from a flash has no root password at all: anyone on the LAN can log in over SSH and into this page. Leave these empty to keep it that way.')
				]),
				row(_('Root password'), pw),
				row(_('Root password again'), pw2)
			]),

			E('div', { 'class': 'cbi-page-actions' }, [ apply ]),
			status
		]);
	},

	handleSaveApply: null,
	handleSave: null,
	handleReset: null
});
