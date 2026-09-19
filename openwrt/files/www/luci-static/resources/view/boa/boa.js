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

		var url = (https ? 'https' : 'http') + '://' + window.location.hostname + ':' + port + '/';

		return E('div', { 'class': 'cbi-map' }, [
			E('h2', {}, [ _('infinite-streaming-boa') ]),
			E('div', { 'class': 'cbi-map-descr' }, [
				_('Per-device link conditioning, served by boad on port %s.').format(port), ' ',
				E('a', { 'href': url, 'target': '_blank', 'rel': 'noopener', 'class': 'btn cbi-button' },
					[ _('Open in a new tab') ]),
				https ? E('p', {}, [
					_('If the frame stays blank, open it in a new tab once and accept the certificate: the browser will not ask from inside a frame.')
				]) : ''
			]),
			E('iframe', {
				'src': url,
				'style': 'width:100%; height:calc(100vh - 220px); min-height:600px; border:0'
			})
		]);
	},

	handleSaveApply: null,
	handleSave: null,
	handleReset: null
});
