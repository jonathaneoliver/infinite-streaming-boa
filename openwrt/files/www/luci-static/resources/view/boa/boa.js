'use strict';
'require view';
'require uci';

// Services -> infinite-streaming-boa: boa's own interface, framed inside LuCI.
//
// boad serves itself on its own ports and sends no X-Frame-Options, so it can
// be embedded as it is. The scheme follows LuCI's: a page reached over https
// may not frame a plain-http one (the browser blocks it as mixed content), so
// on https this frames boad's https port, which uses the same self-signed
// certificate as LuCI.
function portOf(addr, fallback) {
	var p = String(addr || '').replace(/^.*:/, '');
	return p || fallback;
}

return view.extend({
	load: function() {
		return uci.load('boa').catch(function() {});
	},

	render: function() {
		var https = window.location.protocol === 'https:';
		var tlsAddr = uci.get('boa', 'main', 'tls_addr');
		var port = https
			? portOf(tlsAddr == null ? ':8443' : tlsAddr, '')
			: portOf(uci.get('boa', 'main', 'addr'), '8080');

		if (!port)
			return E('div', { 'class': 'cbi-map' }, [
				E('h2', {}, [ _('infinite-streaming-boa') ]),
				E('div', { 'class': 'alert-message warning' }, [
					_('boa has https turned off (boa.main.tls_addr is empty), and a page reached over https cannot show a plain-http one. Open LuCI over http, or set tls_addr.')
				])
			]);

		// THE HOST IS THE BROWSER'S, AND THAT IS AN ASSUMPTION, NOT A FACT.
		//
		// boad runs on the device and serves itself on its own port, so the
		// only address that can be framed is "wherever this page came from,
		// on that port". That is right whenever LuCI is reached directly, and
		// wrong for every indirect route -- an ssh tunnel, a published
		// container port, a reverse proxy -- because those remap the port
		// while leaving the host intact.
		//
		// It cannot be fixed by asking the device instead: uhttpd has no
		// reverse proxy to serve boad same-origin, and the device's own LAN
		// address is no more reachable from an indirect client than this one.
		//
		// What CAN be fixed is the silence. Measured 2026-09-24: LuCI on a
		// container with boa published on 18080 framed whatever answered on
		// :8080 of the viewer's machine -- an unrelated application, presented
		// under boa's heading with no indication anything was wrong. So the
		// URL is now stated, and an unreachable one is reported instead of
		// left as a blank frame. See #367.
		var url = (https ? 'https' : 'http') + '://' + window.location.hostname + ':' + port + '/';

		var frame = E('iframe', {
			'src': url,
			'style': 'width:100%; height:calc(100vh - 220px); min-height:600px; border:0'
		});

		var container = E('div', { 'class': 'cbi-map' }, [
			E('h2', {}, [ _('infinite-streaming-boa') ]),
			E('div', { 'class': 'cbi-map-descr' }, [
				_('Per-device link conditioning, served by boad at %s.').format(url), ' ',
				E('a', { 'href': url, 'target': '_blank', 'rel': 'noopener', 'class': 'btn cbi-button' },
					[ _('Open in a new tab') ]),
				https ? E('p', {}, [
					_('If the frame stays blank, open it in a new tab once and accept the certificate: the browser will not ask from inside a frame.')
				]) : ''
			]),
			frame
		]);

		// A no-cors probe: the response is opaque and unreadable, which is
		// enough -- it resolves when something answered and rejects when
		// nothing did. It cannot tell boa from another service on that port,
		// so the URL above is stated for a human to judge.
		var abort = new AbortController();
		var timer = window.setTimeout(function() { abort.abort(); }, 4000);
		fetch(url, { mode: 'no-cors', signal: abort.signal })
			.then(function() { window.clearTimeout(timer); })
			.catch(function() {
				window.clearTimeout(timer);
				frame.parentNode && frame.parentNode.replaceChild(
					E('div', { 'class': 'alert-message warning' }, [
						E('p', {}, [
							_('Nothing answered at %s.').format(url)
						]),
						E('p', {}, [
							_('This page can only frame boa at the address you reached LuCI on, with boa\'s own port. If you are reaching LuCI through a tunnel, a published container port or a proxy, that port is not mapped and this frame cannot work. Open boa directly on the device instead.')
						])
					]), frame);
			});

		return container;
	},

	handleSaveApply: null,
	handleSave: null,
	handleReset: null
});
