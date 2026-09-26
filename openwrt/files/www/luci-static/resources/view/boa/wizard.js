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
// is its own entry and touches nothing boad owns.
//
// IT ASKS THE QUESTIONS AND `boa-setup` DOES THE WORK. This page used to write
// uci itself -- a second implementation of apply_wizard in JavaScript -- and it
// had already drifted from the shell one in five ways: it installed no drivers
// and no wpad, so netifd configured two access points and tore them both down;
// it set no hostname and no mDNS name; it never checked whether anything was
// actually serving, so it reported `"ubuntu1263" is up on 2 radio(s)` over a
// box with none; and it could not convert to a bridge at all. Measured on the
// x86-64 VM, 2026-09-26. See #406. Everything below collects answers, hands
// them to `boa-setup apply`, and shows what it said.
//
// OVER THE WIRED LAN, NECESSARILY. A device fresh from a flash has no access
// point to join, so Wi-Fi cannot be configured over Wi-Fi, and applying takes
// the radios down and back up. Said on the page, because the person reading it
// is the one who would do that.

var callBoardInfo = rpc.declare({
	object: 'system',
	method: 'board'
});

var callNetworkDevices = rpc.declare({
	object: 'luci-rpc',
	method: 'getNetworkDevices',
	expect: { '': {} }
});

// Fixed paths, matching the exact strings the rpcd ACL allows. Nothing here is
// built from user input: the answers go INSIDE the file, never into the
// command line, because a passphrase in argv is readable by anyone who can
// list /proc for as long as the command runs -- and this one runs for minutes.
var ANSWERS = '/tmp/boa-wizard.conf';
var LOG     = '/tmp/boa-apply.log';
var DONE    = '__BOA_APPLY_DONE';

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

// Follow a detached run and show it as it goes.
//
// `boa-setup apply --background` returns immediately and writes to a log,
// because the install it does outlasts both rpcd's exec timeout and the
// browser's. The page reads that file until the sentinel line appears, so an
// operator watching a three-minute driver install sees it working rather than
// a spinner with nothing behind it.
function followLog(pre, onDone) {
	var stop = false;

	function tick() {
		if (stop)
			return;
		fs.read(LOG).then(function(text) {
			text = text || '';
			var end = text.indexOf(DONE);
			pre.textContent = (end >= 0 ? text.slice(0, end) : text).replace(/\s+$/, '');
			pre.scrollTop = pre.scrollHeight;
			if (end >= 0) {
				stop = true;
				var m = text.slice(end).match(/rc=(\d+)/);
				onDone(m ? parseInt(m[1], 10) : 0, text.slice(0, end));
				return;
			}
			window.setTimeout(tick, 1500);
		}).catch(function() {
			// The log may not exist for the first instant after the exec
			// returns. Keep trying rather than calling it a failure.
			window.setTimeout(tick, 1500);
		});
	}

	tick();
}

return view.extend({
	load: function() {
		return Promise.all([
			// TOLERATED, NOT ASSUMED. A device with no radio driver has no
			// /etc/config/wireless at all, and `uci get wireless` then fails
			// with ubus code 4. Unhandled, that rejection stopped this page at
			// "Loading view..." forever -- on the state EVERY fresh x86 flash
			// is in, because OpenWrt x86 images ship no wireless drivers.
			// Measured 2026-09-26; see #405.
			uci.load('wireless').catch(function() { return null; }),
			callBoardInfo().catch(function() { return {}; }),
			callNetworkDevices().catch(function() { return {}; }),
			uci.load('attendedsysupgrade').catch(function() { return null; }),
			// Somebody is looking at the page, so the unattended countdown must
			// stop. It is two minutes from boot, and it would otherwise rewrite
			// the SSID and passphrase underneath an operator part-way through
			// typing them, then report success for values nobody chose. See
			// #404. Best effort: an older box without this verb must still be
			// able to open the page.
			fs.exec('/usr/sbin/boa-setup', [ 'firstrun', 'cancel' ]).catch(function() { return null; })
		]);
	},

	render: function(data) {
		var board = data[1] || {}, devices = data[2] || {};
		var devs = [], aps = [];

		// uci.sections throws if the config never loaded, which is the
		// no-driver case above.
		try {
			devs = uci.sections('wireless', 'wifi-device');
			aps  = uci.sections('wireless', 'wifi-iface').filter(function(s) {
				return s.mode == 'ap' || s.mode == null;
			});
		} catch (e) { devs = []; aps = []; }

		if (!devs.length)
			return this.renderNoRadios();

		return this.renderForm(board, devices, devs, aps);
	},

	// No wifi-device sections. On x86 that almost always means the radio is
	// sitting there with no driver rather than that there is no radio: the
	// MT7915E in the test VM was present as 14c3:7915 the whole time this page
	// was claiming there was nothing to set up.
	//
	// So this offers to do something about it instead of being a dead end.
	renderNoRadios: function() {
		var out = E('pre', { 'style': 'max-height:22em; overflow:auto; white-space:pre-wrap' });
		var note = E('div', {});

		var go = E('button', { 'class': 'cbi-button cbi-button-apply important', 'click': function(ev) {
			ev.target.disabled = true;
			out.textContent = _('Looking for adapters and installing what they need...');
			fs.exec('/usr/sbin/boa-setup', [ 'install-drivers' ]).then(function(res) {
				out.textContent = (res.stdout || '') + (res.stderr || '');
				note.appendChild(E('div', { 'class': 'alert-message warning' }, [
					E('p', {}, [ _('A driver is usually loaded before its firmware is unpacked, and the probe is not retried, so a reboot is normally needed before the radios appear.') ]),
					E('p', {}, [ _('Reboot, then come back to this page.') ])
				]));
				ev.target.disabled = false;
			}).catch(function(e) {
				out.textContent = _('Could not run boa-setup install-drivers: %s').format(e.message || e);
				ev.target.disabled = false;
			});
		} }, [ _('Install drivers') ]);

		return E('div', { 'class': 'cbi-map' }, [
			E('h2', {}, [ _('boa setup') ]),
			E('div', { 'class': 'alert-message warning' }, [
				E('p', {}, [ _('This device has no wireless configuration yet, which normally means its radio has no driver.') ]),
				E('p', {}, [ _('OpenWrt x86 images ship no wireless drivers at all, so a radio can be physically present and still be invisible here.') ])
			]),
			E('div', { 'class': 'cbi-page-actions' }, [ go ]),
			out,
			note
		]);
	},

	renderForm: function(board, devices, devs, aps) {
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
		var conv    = E('input', { 'type': 'checkbox', 'class': 'cbi-input-checkbox' });

		var status = E('div', {});
		var container;

		var apply = E('button', { 'class': 'cbi-button cbi-button-apply important', 'click': function(ev) {
			ev.target.blur();
			status.innerHTML = '';

			var s = ssid.value.trim(), k = key.value, c = country.value.trim().toUpperCase();
			var wantConvert = conv.checked;

			function bad(msg) {
				status.appendChild(E('div', { 'class': 'alert-message warning' }, [ msg ]));
				return false;
			}

			// Checked here for a quick answer, and again in boa-setup, which is
			// the one that matters: this form is only one of its callers.
			if (!s.length || s.length > 32)
				return bad(_('An SSID is 1 to 32 characters.'));
			if (k.length && (k.length < 8 || k.length > 63))
				return bad(_('A WPA passphrase is 8 to 63 characters. Leave it empty for an open network.'));
			if (k !== key2.value)
				return bad(_('The two passphrases do not match.'));
			// '00' is not a country: hostapd refuses to parse it as a
			// country_code and the access point never starts. An EMPTY value is
			// a different thing entirely -- it is the world domain, and radios
			// come up on it.
			if (c == '00')
				return bad(_('"00" is not a country: hostapd rejects it as an invalid country_code. Leave it empty instead, which is the world domain and works.'));
			if (c.length && !/^[A-Z]{2}$/.test(c))
				return bad(_('A country code is two letters, or empty.'));
			if (pw.value !== pw2.value)
				return bad(_('The two root passwords do not match.'));

			// The same key=value format as boa-firstrun.conf, read by the same
			// parser. A value is written only when it was given, so an empty
			// passphrase means an open network rather than a missing key.
			var setPw = pw.value.length > 0;

			var lines = [ 'ssid=' + s ];
			if (k.length)    lines.push('key=' + k);
			if (c.length)    lines.push('country=' + c);
			if (setPw)       lines.push('root_password=' + pw.value);
			if (wantConvert) lines.push('convert=yes');

			var out = E('pre', { 'style': 'max-height:24em; overflow:auto; white-space:pre-wrap' });
			var panel = E('div', { 'class': 'cbi-map' }, [
				E('h2', {}, [ _('boa setup') ]),
				E('div', { 'class': 'cbi-map-descr' }, [
					_('Installing what this device needs and applying the settings. This can take a few minutes on a device that has no drivers yet — it is installing packages.')
				]),
				out
			]);

			// Cleared from the DOM as soon as they are in the file. They are
			// still in that file until boa-setup reads and deletes it, which is
			// the first thing it does.
			pw.value = pw2.value = key.value = key2.value = '';

			return fs.write(ANSWERS, lines.join('\n') + '\n')
				.then(function() {
					return fs.exec('/usr/sbin/boa-setup', [ 'apply', ANSWERS, '--background' ]);
				})
				.then(function() {
					container.parentNode.replaceChild(panel, container);
					container = panel;

					followLog(out, function(rc, text) {
						var served = /Serving: ([1-9][0-9]*) access point/.exec(text);
						var ok = (rc === 0 && served);

						panel.appendChild(E('div', {
							'class': 'alert-message ' + (ok ? 'success' : 'danger')
						}, ok ? [
							E('p', {}, [ E('strong', {}, [ _('Done.') ]), ' ',
								_('"%s" is serving on %s access point(s).').format(s, served[1]) ]),
							E('p', {}, [ setPw
								? _('The root password is set. The next login will ask for it — including SSH, which until now accepted a blank one.')
								: _('The root password was NOT changed. This device still lets anyone on the LAN log in.') ]),
							wantConvert
								? E('p', {}, [ _('It is now a transparent bridge, so its address came from the upstream router and has changed. The old address stays on the bridge as a rescue address.') ])
								: ''
						] : [
							// LOUDLY, AND ONLY WHEN TRUE. The old page said
							// "up on N radio(s)" from the sections it had just
							// written, which was a success message over a box
							// serving nothing. This reads what boa-setup found.
							E('p', {}, [ E('strong', {}, [ _('It did not finish cleanly.') ]) ]),
							E('p', {}, [ rc === 0
								? _('The settings were written, but no access point came up. The output above says how far it got.')
								: _('boa-setup exited %d. The output above says how far it got.').format(rc) ]),
							E('p', {}, [ _('Check the device with: boa-setup check') ])
						]));

						panel.appendChild(E('div', { 'class': 'cbi-page-actions' }, [
							E('a', { 'class': 'cbi-button cbi-button-apply important',
							         'href': '/cgi-bin/luci/admin/services/boa' }, [ _('Open boa') ]),
							' ',
							E('button', { 'class': 'cbi-button', 'click': function() {
								window.location.reload();
							} }, [ _('Change these settings') ])
						]));

						// The change indicator is client-side. Nothing here
						// writes uci through LuCI any more, but an earlier
						// visit may have left it set.
						try { ui.changes.init(); } catch (e) {}
					});
				})
				.catch(function(e) {
					status.appendChild(E('div', { 'class': 'alert-message danger' }, [
						E('p', {}, [ _('Could not start the setup: %s').format(e.message || e) ]),
						E('p', {}, [ _('Nothing has been changed on the device.') ])
					]));
				});
		} }, [ _('Apply') ]);

		container = E('div', { 'class': 'cbi-map' }, [
			E('h2', {}, [ _('boa setup') ]),
			E('div', { 'class': 'cbi-map-descr' }, [
				_('The first-run settings a device needs before it can serve clients for boa to condition.'), ' ',
				_('Applying installs anything missing — radio drivers, wpad, mDNS — then turns on 802.11k and BSS transition on every access point, which boa needs for measure and steer, and switches off LuCI\'s check-for-firmware-upgrades popup unless you have already answered it.')
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

			E('div', { 'class': 'cbi-section' }, [
				E('h3', {}, [ _('Bridge') ]),
				row(_('Make this a transparent bridge'), conv,
					_('boa conditions traffic passing through a bridge, so it needs this eventually. It deletes the wan interface, moves lan to DHCP and changes this device\'s address — the old one stays on the bridge as a rescue address. Leave it off to do it later with: boa-setup convert.'))
			]),

			E('div', { 'class': 'cbi-page-actions' }, [ apply ]),
			status
		]);

		return container;
	},

	handleSaveApply: null,
	handleSave: null,
	handleReset: null
});
