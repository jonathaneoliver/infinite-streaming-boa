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
// A TRAIL, because the failures this page has produced were all silent ones:
// a frozen log with no error, a success message over a dead box, a jump to the
// wrong step. None of them left anything to read. Every state change is
// announced, so `[boa-wiz]` in the browser console is the page's side of the
// story and `logread -e boa-setup` is the device's.
function trace() {
	try {
		var a = [ '[boa-wiz]' ].concat(Array.prototype.slice.call(arguments));
		console.log.apply(console, a);
	} catch (e) {}
}

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

// ONE PRODUCT, NOT TWO.
//
// The landing page at http://<box>/ is calm: one card, one action, generous
// spacing. It hands over to this page, which was stock LuCI -- dense, and
// bordered in warning yellow -- and the handoff read as leaving the product
// for its admin panel. These few rules carry the landing page's manners
// across, without theming LuCI itself: everything is scoped under .boa-wiz so
// nothing else in LuCI changes.
//
// COLOURS ARE ALPHA OVER currentColor, not fixed greys. LuCI ships light and
// dark themes and a device may be on either, so a hardcoded background is
// wrong half the time. A tint of the theme's own foreground is right on both.
//
// Yellow is spent only on things that can actually go wrong. It used to carry
// "this device has no wireless configuration yet" and "rebooting in 11
// seconds" -- neither a warning -- which left three consecutive screens
// bordered in alarm colour and made the one real caution (configure this over
// the wired port, or you cut yourself off mid-apply) indistinguishable.
var STYLE = '' +
'.boa-wiz .boa-note, .boa-wiz .boa-warn, .boa-wiz .boa-ok, .boa-wiz .boa-bad {' +
'  border:1px solid rgba(127,127,127,.28); border-left-width:3px;' +
'  border-radius:7px; padding:.7rem .9rem; margin:0 0 1rem;' +
'  background:rgba(127,127,127,.07); }' +
'.boa-wiz .boa-note p, .boa-wiz .boa-warn p, .boa-wiz .boa-ok p, .boa-wiz .boa-bad p {' +
'  margin:.25rem 0; }' +
'.boa-wiz .boa-warn { border-left-color:#e0a800; background:rgba(224,168,0,.09); }' +
'.boa-wiz .boa-ok   { border-left-color:#2e8b57; background:rgba(46,139,87,.09); }' +
'.boa-wiz .boa-bad  { border-left-color:#c9302c; background:rgba(201,48,44,.09); }' +
'.boa-wiz pre { border:1px solid rgba(127,127,127,.28); border-radius:7px;' +
'  padding:.7rem .9rem; font-size:12.5px; line-height:1.45; margin:0; }' +
'.boa-wiz pre:empty { display:none; }' +
'.boa-wiz details { margin:0 0 1rem; }' +
'.boa-wiz details > summary { cursor:pointer; opacity:.75; font-size:.92em;' +
'  margin-bottom:.5rem; user-select:none; }' +
'.boa-wiz details > summary:hover { opacity:1; }' +
'.boa-wiz .boa-steps { border-bottom:1px solid rgba(127,127,127,.22);' +
'  padding-bottom:.7rem; }';

function injectStyle() {
	if (document.getElementById('boa-wiz-style'))
		return;
	var s = E('style', { 'id': 'boa-wiz-style' }, [ STYLE ]);
	document.head.appendChild(s);
}

// ONE PROCESS, NOT FOUR PAGES.
//
// Setting up a fresh device is: install drivers, restart, answer the
// questions, apply. Those were four screens that each replaced the last with
// no relationship between them -- and one of them arrived after a reboot,
// which is exactly the moment an operator is least sure anything is still
// going to plan. The spine is on every screen, so each one says where it sits
// in the whole rather than standing alone.
//
// Step 1 is complete when the device HAS radios, which is what installing a
// driver is for. Derived rather than remembered: it is true however the radios
// got there -- a device that shipped with drivers is simply past step 1 -- and
// nothing has to survive the reboot to know it.
function stepBar(active) {
	var names = [ _('Drivers'), _('Settings'), _('Applying'), _('Done') ];
	var items = names.map(function(n, i) {
		var num = i + 1;
		var state = num < active ? 'done' : (num == active ? 'now' : 'todo');
		var dot = {
			done: { bg: '#2e8b57', fg: '#fff', mark: '✓' },
			// OpenWrt amber, matching the landing page this flow starts on.
				now:  { bg: '#e8b10a', fg: '#1c1a12', mark: String(num) },
			todo: { bg: 'transparent', fg: 'inherit', mark: String(num) }
		}[state];

		return E('span', {
			'style': 'display:inline-flex; align-items:center; gap:.45em; margin-right:1.4em; ' +
			         'opacity:' + (state == 'todo' ? '.45' : '1') + '; ' +
			         'font-weight:' + (state == 'now' ? '600' : '400') + ';'
		}, [
			E('span', {
				'style': 'display:inline-flex; align-items:center; justify-content:center;' +
				         'width:1.5em; height:1.5em; border-radius:50%; font-size:.85em;' +
				         'background:' + dot.bg + '; color:' + dot.fg + ';' +
				         'border:1px solid ' + (state == 'todo' ? 'currentColor' : dot.bg) + ';'
			}, [ dot.mark ]),
			n
		]);
	});

	return E('div', {
		'class': 'boa-steps',
		'style': 'display:flex; flex-wrap:wrap; align-items:center; margin:.2em 0 1.1em;'
	}, items);
}

// LuCI's "No password set!" banner, once it has stopped being true.
//
// LuCI renders it when the page loads and never looks again. This wizard sets
// the root password without reloading -- deliberately, because the done panel
// carries the result and a reload would throw it away -- so the warning
// outlives the thing it warns about, and the LAST thing an operator sees after
// closing the open door is a banner telling them it is still open.
//
// Removed only on the path that actually set a password, and matched on the
// banner's own text so nothing else is caught. Measured 2026-09-27: a reload
// clears it by itself, which is what confirms it is stale rather than wrong.
function dismissStalePasswordWarning() {
	try {
		var nodes = document.querySelectorAll('.alert-message, h4');
		for (var i = 0; i < nodes.length; i++) {
			var n = nodes[i];
			if (!/no password set/i.test(n.textContent || ''))
				continue;
			var box = (n.classList && n.classList.contains('alert-message')) ? n : n.parentNode;
			if (box && box.parentNode)
				box.parentNode.removeChild(box);
		}
	} catch (e) {}
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
// SELF-LOCATING, and deliberately so.
//
// This used to be handed a <pre> node and write into it for the life of the
// run. Twice that node turned out not to be in the document -- once it was
// never inserted, once it was replaced -- and the symptom both times was the
// worst one available: the install ran perfectly, the log filled on the box,
// and the page showed a frozen "Looking for adapters..." with no error
// anywhere. Neither cause was ever proven, which is the point: holding a node
// reference across re-renders is a bet, and this does not need to make one.
//
// It finds its element by id each tick, and puts one back if it has gone.
function logEl() {
	var el = document.getElementById('boa-log');
	if (!el) {
		el = E('pre', { 'id': 'boa-log',
		                'style': 'max-height:24em; overflow:auto; white-space:pre-wrap' });
		var host = document.getElementById('boa-panel') || document.querySelector('.boa-wiz');
		if (host) host.appendChild(el);
	}
	return el;
}

// FIRE THE COMMAND WITHOUT JOINING LuCI'S QUEUE.
//
// `boa-setup ... --background` detaches on the box and returns in 0s -- but
// rpcd holds the HTTP request open anyway (measured: an equivalent `ubus call
// file exec` sat for the full 30s). LuCI serialises its XHRs, so that one
// stuck request blocked every later call, `fs.read` was never sent, and the
// log pane sat on its placeholder while the install ran perfectly and finished
// in seconds. The only error anywhere was `XHR request timed out`, 45 seconds
// later, from luci.js. Measured 2026-09-27; this was the cause of every
// "showing me nothing" on this page.
//
// So the trigger goes out as a bare fetch, outside LuCI's plumbing, and is
// abandoned immediately. Nothing waits on it: the log is the result.
// WAIT FOR THE PAGE TO ACTUALLY HAVE THE TREE.
//
// A view returns its elements; LuCI attaches them some time later. Anything
// scheduled with setTimeout(fn, 0) can therefore run BEFORE the tree is in the
// document -- and then every lookup misses, and every element this code
// creates to compensate is appended to nothing and never seen. That is the
// last of the "showing me nothing" failures. Measured 2026-09-27.
function whenMounted(fn) {
	var tries = 0;
	(function check() {
		if (document.querySelector('.boa-wiz')) { fn(); return; }
		if (++tries > 200) { trace('whenMounted: gave up waiting for the view to attach'); return; }
		window.setTimeout(check, 25);
	})();
}

function fireAndForget(command, params) {
	var ctl = ('AbortController' in window) ? new AbortController() : null;
	if (ctl) window.setTimeout(function() { try { ctl.abort(); } catch (e) {} }, 2000);
	try {
		window.fetch('/ubus/', {
			method: 'POST',
			headers: { 'Content-Type': 'application/json' },
			signal: ctl ? ctl.signal : undefined,
			body: JSON.stringify({
				jsonrpc: '2.0', id: 1, method: 'call',
				params: [ L.env.sessionid, 'file', 'exec',
				          { command: command, params: params } ]
			})
		}).catch(function() { return null; });
	} catch (e) { trace('fireAndForget threw', e); }
}

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
			// Machine-readable lines are for this code, not for the operator.
			var el = logEl();
			el.textContent = (end >= 0 ? text.slice(0, end) : text)
				.split('\n').filter(function(l) { return l.slice(0, 2) != '__'; }).join('\n')
				.replace(/\s+$/, '');
			el.scrollTop = el.scrollHeight;
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
				onDone(-2, logEl().textContent);
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
		// NAMED, NOT POSITIONAL. Adding an entry to the array above used to
		// shift the meaning of every later index, and render() went on reading
		// data[4] after the stage had moved to data[5] -- so it saw the
		// firstrun-cancel result, never matched 'reboot-for-drivers', and sent
		// an operator to the settings form on a device whose second radio was
		// still bare. Measured 2026-09-27.
		]).then(function(r) {
			return {
				board:   r[1] || {},
				devices: r[2] || {},
				stage:   r[5] || 'none'
			};
		});
	},

	render: function(data) {
		injectStyle();

		var board = data.board, devices = data.devices;
		var devs = [], aps = [];

		// uci.sections throws if the config never loaded, which is the
		// no-driver case above.
		try {
			devs = uci.sections('wireless', 'wifi-device');
			aps  = uci.sections('wireless', 'wifi-iface').filter(function(s) {
				return s.mode == 'ap' || s.mode == null;
			});
		} catch (e) { devs = []; aps = []; }

		var stage = data.stage;
		trace('render: stage =', stage, '| wifi-devices =', devs.length, '| aps =', aps.length);

		// THE STAGE OUTRANKS THE RADIO COUNT.
		//
		// This used to move on as soon as ANY wifi-device existed, which is
		// wrong on a device with more than one adapter. Measured 2026-09-27
		// with a USB mt7921u beside a PCI MT7915E: the USB radio probes the
		// moment its driver lands and OpenWrt writes a wifi-device for it, so
		// the page jumped to the settings form while the PCI card was still
		// bare and still needed the reboot -- and that card then never came up
		// at all. boa-setup clears the stage only once NO adapter is still
		// waiting, so asking it first is asking the right question.
		if (stage == 'reboot-for-drivers')
			return this.renderNoRadios(stage);

		if (!devs.length)
			return this.renderNoRadios(stage);

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
		into.appendChild(E('div', { 'class': 'boa-note' }, [
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
		trace('reboot: issued, waiting for the device to answer again');
		var line = E('p', { 'class': 'spinning' }, [ _('Rebooting...') ]);
		var since = Date.now();

		into.innerHTML = '';
		into.appendChild(E('div', { 'class': 'boa-note' }, [
			line,
			E('p', {}, [ _('This page comes back by itself when the device answers again, and carries on from step 2. You are not asked to log in again.') ])
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
			trace('reboot: device answered, re-authenticating and reloading');
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
	// STATE IN, SCREEN OUT -- and the screen is rebuilt, never patched.
	//
	// Every failure this page produced came from mutating nodes after render():
	// text that never appeared, a transition the console proved had run while
	// the screen still showed the previous step, elements present one moment and
	// gone the next. The cause was never pinned down -- LuCI owns this DOM and
	// can replace it -- so this stops arguing with it. There is one state
	// object, one paint() that rebuilds the panel from that state, and a
	// heartbeat: if anything replaces the panel, the next tick fills it back in
	// within a second. Nothing here keeps a node between calls.
	renderNoRadios: function(stage) {
		var self = this;
		var st = {
			mode: (stage == 'reboot-for-drivers') ? 'reboot' : 'install',
			sig: '',
			stillBare: false,
			msg: '',
			left: 0,
			since: 0
		};
		var timer = null;

		function set(mode, extra) {
			st.mode = mode;
			if (extra) for (var k in extra) st[k] = extra[k];
			trace('state ->', mode);
			paint(true);
		}

		function note(kind, children) {
			return E('div', { 'class': kind }, children);
		}

		function paint(force) {
			var panel = document.getElementById('boa-panel');
			if (!panel)
				return;
			// Rebuild only when something changed or the log has gone missing.
			// A blind rebuild every tick would yank a button out from under a
			// click -- and the countdown is a button an operator is aiming at.
			var sig = st.mode + '|' + st.left + '|' + st.stillBare + '|' + st.msg;
			if (!force && sig == st.sig && document.getElementById('boa-log'))
				return;
			st.sig = sig;

			var body = [], acts = [];

			if (st.mode == 'install') {
				body.push(note('boa-note', [
					E('p', {}, [ E('strong', {}, [ _('Installing the drivers this device needs.') ]) ]),
					E('p', {}, [ _('Its radios have no driver yet — OpenWrt x86 images ship none, so a radio can be physically present and still be invisible. This fetches packages, so it takes a minute or two.') ])
				]));
			}
			else if (st.mode == 'reboot') {
				body.push(note('boa-note', [
					E('p', {}, [ E('strong', {}, [ st.stillBare
						? _('The drivers are installed. The radios need a reboot to appear.')
						: _('This device is waiting for a reboot to finish installing its drivers.') ]) ]),
					E('p', {}, [ _('A module is loaded before its firmware is unpacked and the probe is never retried, so the radio stays invisible until the device restarts. This is normal and happens once.') ])
				]));
				acts.push(E('button', { 'class': 'cbi-button cbi-button-apply important',
					'click': function() { arm(); } }, [ _('Reboot') ]));
				acts.push(document.createTextNode(' '));
				acts.push(E('button', { 'class': 'cbi-button',
					'click': function() { window.location.reload(); } },
					[ _('I rebooted already — check again') ]));
			}
			else if (st.mode == 'arming') {
				body.push(note('boa-note', [
					E('p', {}, [ _('Rebooting in %d second(s).').format(st.left) ]),
					E('p', {}, [ _('The device goes down for about half a minute. This page waits for it and comes back by itself.') ])
				]));
				acts.push(E('button', { 'class': 'cbi-button cbi-button-apply important',
					'click': function() { go(); } }, [ _('Reboot now') ]));
				acts.push(document.createTextNode(' '));
				acts.push(E('button', { 'class': 'cbi-button',
					'click': function() { set('reboot'); } }, [ _('Cancel') ]));
			}
			else if (st.mode == 'rebooting') {
				body.push(note('boa-note', [
					E('p', { 'class': 'spinning' }, [
						_('Rebooting — waiting for the device to answer again (%ds).')
							.format(Math.round((Date.now() - st.since) / 1000)) ]),
					E('p', {}, [ _('This page comes back by itself and carries on from step 2. You are not asked to log in again.') ])
				]));
			}
			else if (st.mode == 'failed') {
				body.push(note('boa-bad', [
					E('p', {}, [ E('strong', {}, [ _('The drivers could not be installed.') ]) ]),
					E('p', {}, [ st.msg ]),
					E('p', {}, [ _('This step fetches packages, so the device needs a working uplink.') ])
				]));
				acts.push(E('button', { 'class': 'cbi-button cbi-button-apply important',
					'click': function() { install(); } }, [ _('Try again') ]));
			}

			// The log survives the rebuild: its text is read back off the old
			// node, so a repaint never throws away what has streamed in.
			var old = document.getElementById('boa-log');
			var kept = old ? old.textContent : '';
			var log = E('pre', { 'id': 'boa-log',
			                     'style': 'max-height:22em; overflow:auto; white-space:pre-wrap' });
			log.textContent = kept;

			panel.innerHTML = '';
			body.forEach(function(n) { panel.appendChild(n); });
			if (acts.length) {
				var bar = E('div', { 'class': 'cbi-page-actions' });
				acts.forEach(function(n) { bar.appendChild(n); });
				panel.appendChild(bar);
			}
			panel.appendChild(log);
			log.scrollTop = log.scrollHeight;
		}

		function install() {
			trace('install: starting driver install');
			set('install');
			var el = document.getElementById('boa-log');
			if (el) el.textContent = _('Looking for adapters...');

			fireAndForget('/usr/sbin/boa-setup', [ 'install-drivers', '--background' ]);

			followLog(null, function(rc) {
				trace('install: finished rc =', rc);
				if (rc === -1)
					return set('failed', { msg: _('The device never wrote an install log, so nothing was started.') });
				// install-drivers exits non-zero and records the stage when an
				// adapter is installed but still bare, which is the ordinary
				// outcome here rather than a failure.
				set('reboot', { stillBare: rc !== 0 });
			});
		}

		// Fifteen seconds and a way out: rebooting is not undoable once it
		// starts, and the count is also the acknowledgement that the click
		// landed -- the thing that was missing everywhere on this page.
		function arm() {
			set('arming', { left: 15 });
			var t = window.setInterval(function() {
				if (st.mode != 'arming') { window.clearInterval(t); return; }
				st.left--;
				if (st.left <= 0) { window.clearInterval(t); go(); return; }
				paint(true);
			}, 1000);
		}

		function go() {
			trace('reboot: issued, waiting for the device to answer again');
			set('rebooting', { since: Date.now() });
			fireAndForget('/sbin/reboot', []);

			var gone = false;
			function poll() {
				window.fetch('/cgi-bin/luci/', { method: 'HEAD', cache: 'no-store' })
					.then(function() {
						if (gone) { resume(); return; }
						window.setTimeout(poll, 2000);
					})
					.catch(function() { gone = true; window.setTimeout(poll, 2000); });
			}
			window.setTimeout(poll, 5000);
		}

		// Back in without a login screen: the same empty credentials the landing
		// page posts. A reboot ends the session, and setting a password is a
		// later step of this very wizard.
		function resume() {
			trace('reboot: device answered, re-authenticating and reloading');
			var f = E('form', { 'method': 'post',
			                    'action': '/cgi-bin/luci/admin/services/boa_wizard' }, [
				E('input', { 'type': 'hidden', 'name': 'luci_username', 'value': 'root' }),
				E('input', { 'type': 'hidden', 'name': 'luci_password', 'value': '' })
			]);
			document.body.appendChild(f);
			f.submit();
		}

		whenMounted(function() {
			trace('view mounted; mode =', st.mode);
			// THE HEARTBEAT. Cheap -- DOM only, no network -- and it is what makes
			// this immune to whatever replaces the panel underneath it.
			if (!timer)
				timer = window.setInterval(function() {
					paint(st.mode == 'rebooting');
				}, 1000);
			if (st.mode == 'reboot')
				paint(true);
			else
				install();
		});

		return E('div', { 'class': 'cbi-map boa-wiz' }, [
			E('h2', {}, [ _('boa setup') ]),
			stepBar(1),
			E('div', { 'id': 'boa-panel' })
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
		// ON BY DEFAULT. boa conditions traffic passing THROUGH a bridge, so a
		// box that never converts is a box boa cannot do its job on -- every
		// run that left this unticked ended with six of the seven `boa-setup
		// check` failures being the routing-versus-bridging set, and boad
		// declining to start at all. Safe to leave ticked on a box that is
		// already a bridge: convert checks for that itself and does nothing.
		var conv    = E('input', { 'type': 'checkbox', 'class': 'cbi-input-checkbox', 'checked': '' });

		var status = E('div', {});
		var container;

		var apply = E('button', { 'class': 'cbi-button cbi-button-apply important', 'click': function(ev) {
			ev.target.blur();
			status.innerHTML = '';

			var s = ssid.value.trim(), k = key.value, c = country.value.trim().toUpperCase();
			var wantConvert = conv.checked;

			function bad(msg) {
				status.appendChild(E('div', { 'class': 'boa-warn' }, [ msg ]));
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

			// SAME SHAPE AS THE DRIVERS STEP, and for the same reason: this
			// panel used to be built once and then patched from callbacks, and
			// the patches went to nodes LuCI had replaced. One state object, one
			// paint() that rebuilds the whole view from it, and a heartbeat that
			// puts it back if anything takes it away.
			var st = { mode: 'applying', rc: null, text: '', sig: '',
			           ssid: s, country: c, setPw: setPw, wantConvert: wantConvert,
			           boaHref: '', luciHref: '', boaHost: '', served: null,
			           left: 0, goTimer: null };

			// THE LAST STEP TAKES ITSELF. Setup is finished and boa is where
			// the operator was going all along, so the page goes there rather
			// than waiting to be told -- but not instantly, because the panel
			// above it is the only report of what just happened, and the
			// addresses in it are the ones that changed. Fifteen seconds to
			// read it, and a way to stop the clock and stay.
			function startGoCountdown() {
				if (!st.boaHref || st.goTimer)
					return;
				st.left = 15;
				st.goTimer = window.setInterval(function() {
					if (st.mode != 'done' || !st.goTimer) return;
					st.left--;
					if (st.left <= 0) {
						window.clearInterval(st.goTimer);
						st.goTimer = null;
						trace('done: opening boa at', st.boaHref);
						window.location.href = st.boaHref;
						return;
					}
					paintApply(true);
				}, 1000);
			}

			function stopGoCountdown() {
				if (st.goTimer) { window.clearInterval(st.goTimer); st.goTimer = null; }
				st.left = 0;
				paintApply(true);
			}

			function paintApply(force) {
				var host = document.querySelector('.boa-wiz');
				if (!host)
					return;
				// Repaint only when something changed or the panel has gone --
				// rebuilding on every tick would yank a button out from under a
				// click.
				var sig = st.mode + '|' + st.rc + '|' + st.served + '|' + st.left;
				if (!force && sig == st.sig && document.getElementById('boa-log'))
					return;
				st.sig = sig;

				var oldLog = document.getElementById('boa-log');
				var kept = oldLog ? oldLog.textContent : '';

				var ok = (st.mode == 'done');
				var body = [], acts = [];

				if (st.mode == 'applying') {
					body.push(E('div', { 'class': 'cbi-map-descr' }, [
						_('Installing what this device needs and applying the settings. This can take a few minutes on a device that has no drivers yet — it is installing packages.')
					]));
				} else if (ok) {
					body.push(E('div', { 'class': 'cbi-map-descr' }, [
						_('This device is set up. Nothing else is needed here.') ]));
					body.push(E('div', { 'class': 'boa-ok' }, [
						E('p', {}, [ E('strong', {}, [ _('Done.') ]), ' ',
							_('"%s" is serving on %s access point(s).').format(st.ssid, st.served) ]),
						E('p', {}, [ st.setPw
							? _('The root password is set. The next login will ask for it — including SSH, which until now accepted a blank one.')
							: _('The root password was NOT changed. This device still lets anyone on the LAN log in.') ]),
						st.wantConvert
							? E('p', {}, [ _('It is now a transparent bridge, so its address came from the upstream router and has changed. The old address stays on the bridge as a rescue address.') ])
							: ''
					]));
					if (st.boaHost)
						body.push(E('div', { 'class': 'cbi-value-description' }, [
							_('This device is now at '), E('code', {}, [ st.boaHost ]),
							_(' — this page is still on the address setup began with, which the device may have left. Both buttons below go to the new one.')
						]));
					acts.push(E('a', { 'class': 'cbi-button cbi-button-apply important',
					                   'href': st.boaHref || '/cgi-bin/luci/admin/services/boa' },
					                 [ st.left > 0 ? _('Go to boa (%ds)').format(st.left)
					                               : _('Go to boa') ]));
					acts.push(E('a', { 'class': 'cbi-button',
					                   'href': st.luciHref || '/cgi-bin/luci/' }, [ _('Go to OpenWrt') ]));
					if (st.left > 0)
						acts.push(E('button', { 'class': 'cbi-button',
							'click': function() { stopGoCountdown(); } }, [ _('Stay here') ]));
					acts.push(E('span', { 'style': 'flex:1' }));
					acts.push(E('button', { 'class': 'cbi-button', 'click': function() {
						window.location.reload(); } }, [ _('Change these settings') ]));
				} else {
					body.push(E('div', { 'class': 'cbi-map-descr' }, [
						_('Setup did not finish. The output below says how far it got.') ]));
					body.push(E('div', { 'class': 'boa-bad' }, [
						E('p', {}, [ E('strong', {}, [ _('It did not finish cleanly.') ]) ]),
						E('p', {}, [ st.rc === -1
							? _('Nothing started: the device never wrote a setup log. Nothing has been changed.')
							: st.rc === -2
							? _('The device stopped answering while it was working, and did not come back. If you ticked the bridge box its address has changed — try the rescue address, 192.168.1.1.')
							: st.rc === 0
							? _('The settings were written, but no access point came up. The output above says how far it got.')
							: _('boa-setup exited %d. The output above says how far it got.').format(st.rc) ]),
						E('p', {}, [ _('Check the device with: boa-setup check, and boa-setup logs for the whole run.') ])
					]));
					acts.push(E('button', { 'class': 'cbi-button', 'click': function() {
						window.location.reload(); } }, [ _('Change these settings') ]));
				}

				var log = E('pre', { 'id': 'boa-log',
				                     'style': 'max-height:24em; overflow:auto; white-space:pre-wrap' });
				log.textContent = kept;

				host.innerHTML = '';
				host.appendChild(E('h2', {}, [ _('boa setup') ]));
				host.appendChild(stepBar(ok ? 4 : 3));
				body.forEach(function(nd) { if (nd) host.appendChild(nd); });
				if (ok) {
					// The transcript is history now, so it folds away.
					host.appendChild(E('details', {}, [
						E('summary', {}, [ _('Show what it did') ]), log ]));
				} else {
					host.appendChild(log);
				}
				if (acts.length) {
					var bar = E('div', { 'class': 'cbi-page-actions',
					                     'style': 'display:flex; gap:.6rem; align-items:center;' });
					acts.forEach(function(nd) { bar.appendChild(nd); });
					host.appendChild(bar);
				}
				log.scrollTop = log.scrollHeight;

				// LuCI drew this before the wizard closed the door.
				if (ok && st.setPw)
					dismissStalePasswordWarning();
			}

			// Cleared from the DOM as soon as they are in the file. They are
			// still in that file until boa-setup reads and deletes it, which is
			// the first thing it does.
			pw.value = pw2.value = key.value = key2.value = '';

			return fs.write(ANSWERS, lines.join('\n') + '\n')
				.then(function() {
					paintApply(true);
					window.setInterval(function() { paintApply(false); }, 1000);

					// A trigger, not a result: rpcd holds the exec call open and
					// convert restarts the network under it. The log is the truth.
					fireAndForget('/usr/sbin/boa-setup', [ 'apply', ANSWERS, '--background' ]);

					trace('apply: started, following the log');
					followLog(null, function(rc, text) {
						trace('apply: finished rc =', rc);
						var served = /Serving: ([1-9][0-9]*) access point/.exec(text || '');
						var m = /__BOA_URL (\S+)/.exec(text || '');
						if (m) {
							st.boaHref = m[1];
							try {
								var u = new URL(m[1]);
								st.boaHost  = u.hostname;
								st.luciHref = u.protocol + '//' + u.hostname + '/cgi-bin/luci/';
							} catch (e) {}
						}
						st.rc = rc;
						st.served = served ? served[1] : null;
						st.mode = (rc === 0 && served) ? 'done' : 'failed';
						paintApply(true);
						if (st.mode == 'done')
							startGoCountdown();
						try { ui.changes.init(); } catch (e) {}
					});
				})
				// Only fs.write reaches here, and it runs before anything on the
				// device has been touched -- the one place this page can honestly
				// say nothing changed.
				.catch(function(e) {
					status.appendChild(E('div', { 'class': 'boa-bad' }, [
						E('p', {}, [ _('Could not write the answers to the device: %s').format(e.message || e) ]),
						E('p', {}, [ _('Nothing has been changed.') ])
					]));
				});
		} }, [ _('Apply') ]);

		container = E('div', { 'class': 'cbi-map boa-wiz' }, [
			E('h2', {}, [ _('boa setup') ]),
			stepBar(2),
			E('div', { 'class': 'cbi-map-descr' }, [
				_('The first-run settings a device needs before it can serve clients for boa to condition.'), ' ',
				_('Applying installs anything missing — radio drivers, wpad, mDNS — then turns on 802.11k and BSS transition on every access point, which boa needs for measure and steer, and switches off LuCI\'s check-for-firmware-upgrades popup unless you have already answered it.')
			]),
			E('div', { 'class': 'boa-warn' }, [
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
