// Unit checks for the OpenFi LuCI view: curve rules, legacy defaults and status decoding.
// Runs with plain Node.js; LuCI modules are replaced by small stubs.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

const source = fs.readFileSync('package/mtk/applications/luci-app-openfi/htdocs/luci-static/resources/view/openfi/settings.js', 'utf8');
// Minimal String.format for %s, %d and %%, as provided by LuCI.
String.prototype.format = function(...args) {
	let i = 0;
	return this.replace(/%([sd%])/g, (m, t) => t === '%' ? '%' : String(args[i++]));
};

function load(stubs) {
	const context = vm.createContext({
		view: { extend: o => o },
		form: {}, poll: {},
		_: s => s,
		E: () => ({}),
		L: { resolveDefault: (p, d) => Promise.resolve(p).catch(() => d) },
		...stubs
	});
	return vm.runInContext('(function(){' + source + '\n})()', context);
}

(async () => {
	// Curve rules.
	const view = load({ uci: { get: () => undefined }, fs: {}, rpc: { declare: () => () => Promise.resolve() } });
	const curve = (t, s) => t.map((v, i) => ({ t: v, s: s[i] }));
	const ok = curve([55, 58, 61, 65], [55, 70, 85, 100]);
	assert.equal(view.curveError(ok, 5, 85), '');
	assert.match(view.curveError(ok, 60, 85), /最低稳定输出/, 'outputs below the floor are rejected');
	assert.match(view.curveError(curve([55, 56, 61, 65], [55, 70, 85, 100]), 5, 85), /2°C/, 'points need 2°C spacing');
	assert.match(view.curveError(curve([55, 58, 61, 65], [55, 70, 60, 100]), 5, 85), /不能低于/, 'output must not fall');
	assert.match(view.curveError(ok, 5, 66), /第四个点/, 'full speed must clear the last point');
	assert.match(view.curveError(curve([55, NaN, 61, 65], [55, 70, 85, 100]), 5, 85), /整数/);
	assert.equal(view.integer('6.5', null, 0, 100), null);
	assert.equal(view.integer('', 7, 0, 100), 7);
	assert.equal(view.integer('100', null, 0, 100), 100);

	// Defaults derived from the three-level firmware configuration.
	const legacy = { level: '1', cpu_temp_low: '50', cpu_temp_high: '68' };
	const old = load({ uci: { get: (c, s, o) => legacy[o] }, fs: {}, rpc: { declare: () => () => Promise.resolve() } });
	const d = old.legacyDefaults();
	assert.equal(d.floor, '55');
	assert.deepEqual(Object.values(d.points), ['50', '55', '56', '70', '62', '85', '68', '100']);
	const narrow = load({ uci: { get: (c, s, o) => ({ cpu_temp_low: '60', cpu_temp_high: '62', min_speed: '5' })[o] }, fs: {}, rpc: { declare: () => () => Promise.resolve() } });
	assert.equal(narrow.legacyDefaults().points.temp1, '55', 'too narrow legacy range falls back to 55–65°C');

	// Status decoding.
	function statusView({ fan, now, running = true, pwm = true, sensor }) {
		const files = { '/var/run/openfi-fan.json': fan, '/sys/class/thermal/thermal_zone0/temp': sensor };
		const rpcs = {
			getUnixtime: () => now instanceof Error ? Promise.reject(now) : Promise.resolve(now),
			list: () => Promise.resolve({ openfi: { instances: { fan: { running } } } })
		};
		return load({
			uci: { get: () => undefined },
			fs: {
				read: path => files[path] == null ? Promise.reject(new Error('NotFoundError')) : Promise.resolve(files[path]),
				stat: () => pwm ? Promise.resolve({ type: 'file' }) : Promise.reject(new Error('NotFoundError'))
			},
			rpc: { declare: ({ method }) => rpcs[method] }
		});
	}
	const fresh = JSON.stringify({ updated: 1000, period: 5, mode: 'auto', cpu: 52, output: 38, target: 40, reason: 'auto' });
	let s = await statusView({ fan: fresh, now: 1004 }).readStatus();
	assert.equal(s.stale, false);
	assert.equal(s.output, 38);
	s = await statusView({ fan: fresh, now: 1100, sensor: '48500\n' }).readStatus();
	assert.equal(s.stale, true, 'old data is stale');
	assert.equal(s.cpu, 48, 'stale status shows the live sensor reading');
	assert.equal(s.output, null);
	s = await statusView({ fan: fresh, now: 1100 }).readStatus();
	assert.equal(s.cpu, null, 'an unreadable sensor leaves the temperature empty');
	s = await statusView({ fan: fresh, now: new Error('denied') }).readStatus();
	assert.equal(s.stale, true, 'without the router clock the data cannot be trusted');
	s = await statusView({ fan: 'not json', now: 1004, running: false, pwm: false }).readStatus();
	assert.equal(s.stale, true);
	assert.equal(s.running, false);
	assert.equal(s.pwm_present, false);

	// Menu and ACL files are valid JSON and cover the real sysfs targets.
	const app = 'package/mtk/applications/luci-app-openfi/root/usr/share/';
	JSON.parse(fs.readFileSync(app + 'luci/menu.d/luci-app-openfi.json', 'utf8'));
	const acl = JSON.parse(fs.readFileSync(app + 'rpcd/acl.d/luci-app-openfi.json', 'utf8'))['luci-app-openfi'];
	for (const path of ['/tmp/run/openfi-fan.json', '/sys/devices/virtual/thermal/thermal_zone0/temp'])
		assert.ok(acl.read.file[path], path + ' must be readable after rpcd resolves the symlink');
	assert.deepEqual(acl.write, { uci: ['openfi'] }, 'the view only writes its own configuration');

	console.log('OpenFi view: curve rules, legacy defaults, status decoding and ACL files verified.');
})().catch(e => { console.error(e); process.exitCode = 1; });
