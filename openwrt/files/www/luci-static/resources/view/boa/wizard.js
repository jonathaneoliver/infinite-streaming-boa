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
	// The network goes down and comes back during convert, so a read failing
	// is expected and must not end the follow. Only a run of them from the
	// very start means nothing was ever started.
	var misses = 0, everRead = false;

	function tick() {
		if (stop)
			return;
		fs.read(LOG).then(function(text) {
			everRead = true;
			misses = 0;
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
			misses++;
			// Nothing ever appeared: the run did not start. Said after 30s
			// rather than spinning forever.
			if (!everRead && misses > 20) {
				stop = true;
				onDone(-1, '');
				return;
			}
			// It was running and the box went away -- which is what convert
			// does. Keep looking: it comes back on the rescue address.
			if (everRead && misses > 120) {
				stop = true;
				onDone(-2, pre.textContent);
				return;
			}
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
			fs.exec('/usr/sbin/boa-setup', [ 'firstrun', 'cancel' ]).catch(function() { return null; }),
			// Where setup had got to before the last reboot. Installing a
			// driver needs one, and the restart takes this page with it, so
			// without this an operator comes back to the screen they started
			// from with no way to tell their last click did anything.
			fs.exec('/usr/sbin/boa-setup', [ 'stage', 'get' ])
				.then(function(r) { return (r.stdout || '').trim(); })
				.catch(function() { return 'none'; })
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
			return this.renderNoRadios(data[4] || 'none');

		return this.renderForm(board, devices, devs, aps);
	},

	// Reboot, and wait for the box to come back.
	//
	// The request dies with the box, which is expected and not an error, so
	// nothing here waits on it. What it waits on is the device answering
	// again, and then it reloads -- so the operator ends up on the next stage
	// of setup rather than on a dead tab they have to think about.
	// Fifteen seconds before it actually goes, and a way out.
	//
	// Rebooting a box is not undoable once it starts, and the button sits on a
	// page an operator may have opened to read rather than to act. The count
	// is also the acknowledgement that the click landed -- the thing that was
	// missing everywhere else on this page.
	armReboot: function(into, onCancel) {
		var self = this, left = 15, timer = null;
		var line = E('p', {});

		function paint() {
			line.textContent = _('Rebooting in %d second(s).').format(left);
		}

		var cancel = E('button', { 'class': 'cbi-button', 'click': function() {
			window.clearInterval(timer);
			onCancel();
		} }, [ _('Cancel') ]);

		var now = E('button', { 'class': 'cbi-button cbi-button-apply important', 'click': function() {
			window.clearInterval(timer);
			self.rebootAndWait(into);
		} }, [ _('Reboot now') ]);

		into.innerHTML = '';
		into.appendChild(E('div', { 'class': 'alert-message warning' }, [
			line,
			E('p', {}, [ _('The device goes down for about half a minute. This page waits for it and comes back by itself.') ]),
			E('div', { 'class': 'cbi-page-actions' }, [ now, ' ', cancel ])
		]));
		paint();

		timer = window.setInterval(function() {
			left--;
			if (left <= 0) {
				window.clearInterval(timer);
				self.rebootAndWait(into);
				return;
			}
			paint();
		}, 1000);
	},

	rebootAndWait: function(into) {
		var line = E('p', { 'class': 'spinning' }, [ _('Rebooting...') ]);
		var since = Date.now();

		into.innerHTML = '';
		into.appendChild(E('div', { 'class': 'alert-message warning' }, [
			line,
			E('p', {}, [ _('This page comes back by itself when the device answers again. You will be asked to log in: a reboot ends the session.') ])
		]));

		// A visible clock, because the one question this screen has to answer
		// is "is anything actually happening".
		window.setInterval(function() {
			var s = Math.round((Date.now() - since) / 1000);
			line.textContent = _('Rebooting — waiting for the device to answer again (%ds).').format(s);
		}, 1000);

		fs.exec('/sbin/reboot').catch(function() { return null; });

		// Long enough that the box has actually gone down before the first
		// poll: asking too early gets an answer from a device that is about
		// to stop answering, and the page reloads into the reboot it just
		// asked for.
		var gone = false;
		function poll() {
			window.fetch('/cgi-bin/luci/', { method: 'HEAD', cache: 'no-store' })
				.then(function() {
					if (gone) { resume(); return; }
					window.setTimeout(poll, 2000);
				})
				.catch(function() {
					gone = true;
					window.setTimeout(poll, 2000);
				});
		}
		window.setTimeout(poll, 5000);

		// BACK IN WITHOUT A LOGIN SCREEN.
		//
		// A reboot ends the session, so a plain reload lands on LuCI's
		// "Authorization Required" -- asking for a password that does not
		// exist yet, since setting one is a later step of this very wizard.
		//
		// So the same empty credentials the landing page uses are posted
		// again, straight at this page. This grants nothing: a device with no
		// root password is already open to anyone who can reach it. If one HAS
		// been set by now the POST is refused and LuCI asks properly, which is
		// the right outcome rather than a fallback.
		function resume() {
			var f = E('form', {
				'method': 'post',
				'action': '/cgi-bin/luci/admin/services/boa_wizard'
			}, [
				E('input', { 'type': 'hidden', 'name': 'luci_username', 'value': 'root' }),
				E('input', { 'type': 'hidden', 'name': 'luci_password', 'value': '' })
			]);
			document.body.appendChild(f);
			f.submit();
		}
	},

	// No wifi-device sections. On x86 that almost always means the radio is
	// sitting there with no driver rather than that there is no radio: the
	// MT7915E in the test VM was present as 14c3:7915 the whole time this page
	// was claiming there was nothing to set up.
	//
	// So this offers to do something about it instead of being a dead end.
	renderNoRadios: function(stage) {
		var self = this;
		var out     = E('pre', { 'style': 'max-height:22em; overflow:auto; white-space:pre-wrap' });
		var actions = E('div', { 'class': 'cbi-page-actions' });
		var intro   = E('div', {});

		// AFTER THE DRIVER INSTALL, BEFORE THE REBOOT. The stage survived the
		// page, so this picks up where the operator left off instead of
		// offering to install drivers that are already installed.
		function rebootState(stillBare) {
			intro.innerHTML = '';
			intro.appendChild(E('div', { 'class': 'alert-message warning' }, [
				E('p', {}, [ E('strong', {}, [ stillBare
					? _('The drivers are installed. The radios need a reboot to appear.')
					: _('This device is waiting for a reboot to finish installing its drivers.') ]) ]),
				E('p', {}, [ _('A module is loaded before its firmware is unpacked and the probe is never retried, so the radio stays invisible until the device restarts. This is normal and happens once.') ])
			]));
			actions.innerHTML = '';
			actions.appendChild(E('button', {
				'class': 'cbi-button cbi-button-apply important',
				'click': function() {
					actions.innerHTML = '';
					self.armReboot(intro, function() { rebootState(stillBare); });
				}
			}, [ _('Reboot') ]));
			actions.appendChild(document.createTextNode(' '));
			actions.appendChild(E('button', { 'class': 'cbi-button', 'click': function() {
				window.location.reload();
			} }, [ _('I rebooted already — check again') ]));
		}

		function installState() {
			intro.innerHTML = '';
			intro.appendChild(E('div', { 'class': 'alert-message warning' }, [
				E('p', {}, [ _('This device has no wireless configuration yet, which normally means its radio has no driver.') ]),
				E('p', {}, [ _('OpenWrt x86 images ship no wireless drivers at all, so a radio can be physically present and still be invisible here.') ])
			]));
			actions.innerHTML = '';
			actions.appendChild(E('button', { 'class': 'cbi-button cbi-button-apply important', 'click': function(ev) {
				ev.target.disabled = true;
				// It takes as long as it takes to fetch and unpack 28
				// packages, and nothing on screen would otherwise change for
				// all of it. Said, so the wait reads as work.
				out.textContent = _('Looking for adapters and installing what they need. This fetches packages, so it can take a minute or two...');
				fs.exec('/usr/sbin/boa-setup', [ 'install-drivers' ]).then(function(res) {
					out.textContent = (res.stdout || '') + (res.stderr || '');
					// install-drivers exits non-zero and records the stage
					// when an adapter is installed but still bare, which is
					// the ordinary outcome here rather than a failure.
					rebootState(true);
				}).catch(function(e) {
					out.textContent = _('Could not run boa-setup install-drivers: %s').format(e.message || e);
					ev.target.disabled = false;
				});
			} }, [ _('Install drivers') ]));
		}

		if (stage == 'reboot-for-drivers')
			rebootState(false);
		else
			installState();

		return E('div', { 'class': 'cbi-map' }, [
			E('h2', {}, [ _('boa setup') ]),
			intro,
			actions,
			out
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
					container.parentNode.replaceChild(panel, container);
					container = panel;

					// FIRED, AND DELIBERATELY NOT WAITED ON.
					//
					// `--background` detaches properly -- measured on the box,
					// it returns in 0s with the child still running -- but rpcd
					// holds the exec CALL open past that, and the last thing
					// this run does is convert, which restarts the network. The
					// XHR then dies under the browser and the old code reported
					// `XHR request aborted by browser / Nothing has been changed
					// on the device` over a device that had just been fully and
					// correctly set up. Measured 2026-09-26.
					//
					// So the request is a trigger, not a result. The log is the
					// source of truth, and following it survives the network
					// going away and coming back.
					fs.exec('/usr/sbin/boa-setup', [ 'apply', ANSWERS, '--background' ])
						.catch(function() { return null; });

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
							E('p', {}, [ rc === -1
								? _('Nothing started: the device never wrote a setup log. Nothing has been changed.')
								: rc === -2
								? _('The device stopped answering while it was working, and did not come back. If you ticked the bridge box its address has changed — try the rescue address, 192.168.1.1.')
								: rc === 0
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
				// Only fs.write can reach here, and it runs before anything on
				// the device has been touched -- so this is the one place the
				// page can honestly say nothing changed.
				.catch(function(e) {
					status.appendChild(E('div', { 'class': 'alert-message danger' }, [
						E('p', {}, [ _('Could not write the answers to the device: %s').format(e.message || e) ]),
						E('p', {}, [ _('Nothing has been changed.') ])
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
