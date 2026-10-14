'use strict';
'require view';
'require form';
'require fs';
'require rpc';
'require poll';
'require uci';

const FAN_STATE = '/var/run/openfi-fan.json';
const CPU_SENSOR = '/sys/class/thermal/thermal_zone0/temp';
const PWM_EXPORT = '/sys/class/pwm/pwmchip0/export';

const callServiceList = rpc.declare({ object: 'service', method: 'list', params: [ 'name' ], expect: { '': {} } });
const callUnixtime = rpc.declare({ object: 'luci', method: 'getUnixtime', expect: { result: 0 } });

const REASONS = {
	auto: _('自动温控运行中，升温加速、降温缓降。'),
	manual: _('手动输出运行中，CPU 高温保护仍然生效。'),
	overheat: _('CPU 已达到强制全速温度，风扇全速运行。'),
	sensor_fault: _('CPU 温度读取失败，已请求全速保护。'),
	pwm_fault: _('PWM 控制异常，无法确认输出，请检查风扇服务。'),
	stopped: _('风扇服务已停止，退出前已请求全速。'),
	starting: _('正在全速助推，帮助风扇可靠启动。')
};

/* Parse an integer within [lower, upper]; anything else yields the fallback. */
function integer(value, fallback, lower, upper) {
	if (value == null || String(value).trim() === '')
		return fallback;
	const n = Number(value);
	return Number.isInteger(n) && n >= lower && n <= upper ? n : fallback;
}

/* Check the four-point curve against the output floor and the full-speed temperature.
 * Returns '' when the combination is valid, otherwise the message to show. */
function curveError(points, floor, limit) {
	if (!Number.isInteger(floor) || floor < 5 || floor > 100)
		return _('最低稳定输出需为 5–100% 的整数。');
	if (!Number.isInteger(limit) || limit < 60 || limit > 100)
		return _('强制全速温度需为 60–100°C 的整数。');
	for (let i = 0; i < 4; i++) {
		const p = points[i];
		if (!Number.isInteger(p.t) || p.t < 20 || p.t > 95)
			return _('四个温度点需为 20–95°C 的整数。');
		if (!Number.isInteger(p.s) || p.s < floor || p.s > 100)
			return _('每个点的输出需介于最低稳定输出与 100% 之间。');
		if (i && p.t < points[i - 1].t + 2)
			return _('相邻温度点必须至少相隔 2°C，不能交叉。');
		if (i && p.s < points[i - 1].s)
			return _('后一个点的输出不能低于前一个点。');
	}
	if (limit < points[3].t + 2)
		return _('强制全速温度至少应高于第四个点 2°C。');
	return '';
}

/* Before the four-point curve existed the fan used a three-level setting and a
 * low/high temperature pair; derive the initial floor and points from them. */
function legacyDefaults() {
	const level = uci.get('openfi', 'fan', 'level');
	const floor = { '0': 43, '1': 55, '2': 100 }[level] ?? 5;
	let low = integer(uci.get('openfi', 'fan', 'cpu_temp_low'), 55, 20, 95);
	let high = integer(uci.get('openfi', 'fan', 'cpu_temp_high'), 65, 25, 100);
	if (high - low < 6) { low = 55; high = 65; }
	const minimum = integer(uci.get('openfi', 'fan', 'min_speed'), floor, 5, 100);
	const points = {};
	for (let i = 1; i <= 4; i++) {
		points['temp' + i] = String(low + Math.floor((high - low) * (i - 1) / 3));
		points['speed' + i] = String(minimum + Math.floor((100 - minimum) * (i - 1) / 3));
	}
	return { floor: String(floor), points };
}

/* Fan state as written by op_fan.sh; old or missing data is reported as stale.
 * rpcd checks file grants on both the requested path and its resolved target,
 * so the ACL lists /var/run and /sys/class paths together with their real paths. */
function readStatus() {
	return Promise.all([
		L.resolveDefault(fs.read(FAN_STATE), ''),
		L.resolveDefault(callUnixtime(), 0),
		L.resolveDefault(callServiceList('openfi'), {}),
		L.resolveDefault(fs.stat(PWM_EXPORT), null)
	]).then(([ text, now, services, pwm ]) => {
		let data = {};
		try { data = JSON.parse(text) || {}; } catch (e) {}
		const updated = Number(data.updated), period = Number(data.period) || 5;
		data.stale = !Number.isFinite(updated) || !now || now - updated > Math.max(15, period * 3);
		data.pwm_present = pwm != null;
		data.running = services?.openfi?.instances?.fan?.running === true;
		if (!data.stale)
			return data;
		data.output = data.target = null;
		return L.resolveDefault(fs.read(CPU_SENSOR), '').then(raw => {
			const milli = Number(String(raw).trim());
			data.cpu = raw !== '' && milli >= 0 && milli <= 150000 ? Math.floor(milli / 1000) : null;
			return data;
		});
	});
}

function statusSection() {
	const cells = {};
	const row = (key, title) => E('tr', { 'class': 'tr' }, [
		E('td', { 'class': 'td left', 'width': '33%' }, title),
		cells[key] = E('td', { 'class': 'td left' }, '—')
	]);
	const node = E('div', { 'class': 'cbi-section' }, [
		E('h3', _('运行状态')),
		E('table', { 'class': 'table' }, [
			row('cpu', _('CPU 温度')),
			row('output', _('风扇输出')),
			row('mode', _('控制模式')),
			row('health', _('状态'))
		])
	]);
	cells.health.setAttribute('aria-live', 'polite');
	cells.health.textContent = _('正在读取风扇服务状态…');
	const percent = v => typeof v === 'number' ? v + '%' : '—';

	function update(data) {
		cells.cpu.textContent = typeof data.cpu === 'number' ? data.cpu + '°C' : '—';
		if (data.stale) {
			cells.output.textContent = cells.mode.textContent = '—';
			cells.health.textContent = !data.pwm_present ? _('未找到 PWM 控制器，请检查内核和设备树。') :
				!data.running ? _('风扇进程未运行。请检查 OpenFi 启动项及系统日志中的 openfi-fan 错误。') :
				_('风扇进程正在运行，但状态未更新。请检查 PWM 初始化和系统日志。');
			return;
		}
		// Output is the PWM duty ratio, not a measured fan speed.
		const output = data.reason === 'pwm_fault' ? '—' : percent(data.output);
		cells.output.textContent = data.target != null && data.target !== data.output
			? output + _('（目标 %s）').format(percent(data.target)) : output;
		cells.mode.textContent = data.mode === 'manual' ? _('手动') : _('自动温控');
		cells.health.textContent = REASONS[data.reason] || _('正在更新状态…');
	}

	return { node, refresh: () => readStatus().then(update) };
}

/* Four-point curve chart and number grid. Both edit the hidden temp/speed options. */
function curveEditor(m, sid) {
	const ns = 'http://www.w3.org/2000/svg';
	const x = t => 62 + (t - 20) * 8.175, y = s => 276 - s * 2.4;
	function svg(tag, attrs, parent) {
		const n = document.createElementNS(ns, tag);
		for (const k in attrs) n.setAttribute(k, attrs[k]);
		if (parent) parent.appendChild(n);
		return n;
	}

	const chart = svg('svg', { 'viewBox': '0 0 760 340', 'aria-label': _('四点风扇曲线编辑器') });
	const grid = svg('g', {}, chart);
	const floorLine = svg('path', { 'fill': 'none', 'stroke': 'currentColor', 'stroke-opacity': '.4', 'stroke-dasharray': '3 5' }, chart);
	const danger = svg('g', { 'class': 'of-danger' }, chart);
	const safetyLine = svg('path', { 'fill': 'none', 'stroke': 'currentColor', 'stroke-dasharray': '6 5' }, danger);
	const safetyLabel = svg('text', { 'y': 22, 'text-anchor': 'end' }, danger);
	const accent = svg('g', { 'class': 'of-accent' }, chart);
	const area = svg('path', { 'fill': 'currentColor', 'fill-opacity': '.08' }, accent);
	const line = svg('path', { 'fill': 'none', 'stroke': 'currentColor', 'stroke-width': '2.5', 'stroke-linejoin': 'round' }, accent);
	const handlesGroup = svg('g', {}, accent);
	svg('text', { 'x': 390, 'y': 329, 'text-anchor': 'middle' }, chart).textContent = _('CPU 温度（°C）');
	svg('text', { 'x': 8, 'y': 18 }, chart).textContent = _('PWM 输出');
	for (let t = 20; t <= 100; t += 10) {
		svg('path', { 'd': 'M' + x(t) + ' 36V276', 'stroke': 'currentColor', 'stroke-opacity': '.1' }, grid);
		svg('text', { 'x': x(t), 'y': 299, 'text-anchor': 'middle' }, grid).textContent = t;
	}
	for (let s = 0; s <= 100; s += 25) {
		svg('path', { 'd': 'M62 ' + y(s) + 'H716', 'stroke': 'currentColor', 'stroke-opacity': '.1' }, grid);
		svg('text', { 'x': 52, 'y': y(s) + 4, 'text-anchor': 'end' }, grid).textContent = s + '%';
	}

	const inputs = { temp: [], speed: [] };
	const cell = (kind, i, min, max, label) => inputs[kind][i] = E('input', {
		'type': 'number', 'class': 'cbi-input-text', 'id': 'of-' + kind + (i + 1),
		'min': min, 'max': max, 'step': 1, 'aria-label': label.format(i + 1)
	});
	const pointGrid = E('div', { 'class': 'of-points' }, [
		E('span'), ...[0, 1, 2, 3].map(i => E('span', _('点 %d').format(i + 1))),
		E('span', _('温度（°C）')), ...[0, 1, 2, 3].map(i => cell('temp', i, 20, 95, _('点 %d 温度'))),
		E('span', _('输出（%）')), ...[0, 1, 2, 3].map(i => cell('speed', i, 5, 100, _('点 %d 输出')))
	]);
	const error = E('div', { 'id': 'of-curve-error', 'class': 'alert-message warning', 'role': 'status', 'aria-live': 'polite', 'hidden': '' });
	const caption = E('div', { 'class': 'cbi-value-description' });
	const node = E('div', { 'id': 'of-editor' }, [
		E('style', [
			'#of-editor{max-width:760px;margin:0 0 1em}',
			'#of-editor svg{display:block;width:100%;height:auto;overflow:visible;touch-action:none;user-select:none}',
			'#of-editor svg text{fill:currentColor;font-size:12px;opacity:.65}',
			'#of-editor .of-accent{color:var(--brand,var(--primary-color-high,#1a73e8))}',
			'#of-editor .of-danger{color:var(--danger,var(--error-color-high,#d93025))}',
			'#of-editor .of-handle{cursor:grab;fill:Canvas;stroke:currentColor;stroke-width:2.5}',
			'#of-editor .of-handle:focus{outline:none;stroke-width:4}',
			'#of-editor .of-handle:active{cursor:grabbing}',
			'#of-editor .of-points{display:grid;grid-template-columns:auto repeat(4,minmax(0,6em));justify-content:start;gap:8px 10px;align-items:center;margin:0 0 1em}',
			'#of-editor .of-points input{width:100%;min-width:0;box-sizing:border-box}'
		].join('\n')),
		chart, pointGrid, error, caption,
		E('div', { 'class': 'cbi-value-description' },
			_('拖动圆点或在表格中输入数值，选中圆点后可用方向键微调（按住 Shift 每次 5）。相邻温度点至少相隔 2°C，输出随温度不降。保存并应用后生效。'))
	]);

	const option = name => m.lookupOption(name, sid)?.[0];
	function number(name) {
		const v = option(name)?.formvalue(sid);
		return v == null || String(v).trim() === '' ? NaN : Number(v);
	}
	const points = () => [1, 2, 3, 4].map(i => ({ t: number('temp' + i), s: number('speed' + i) }));
	const handles = [];

	function render() {
		const p = points(), floor = number('min_speed'), limit = number('emergency_temp');
		const message = curveError(p, floor, limit);
		error.textContent = message;
		error.hidden = !message;
		p.forEach((v, i) => {
			inputs.temp[i].setCustomValidity(message);
			inputs.speed[i].setCustomValidity(message);
			const valid = Number.isFinite(v.t) && Number.isFinite(v.s);
			handles[i].group.setAttribute('visibility', valid ? 'visible' : 'hidden');
			if (valid) {
				handles[i].circle.setAttribute('cx', x(v.t));
				handles[i].circle.setAttribute('cy', y(v.s));
				handles[i].circle.setAttribute('aria-valuenow', v.t);
				handles[i].circle.setAttribute('aria-valuetext', v.t + '°C, ' + v.s + '%');
				handles[i].label.setAttribute('x', x(v.t));
				handles[i].label.setAttribute('y', y(v.s) + 4);
			}
		});
		floorLine.setAttribute('d', Number.isFinite(floor) ? 'M62 ' + y(floor) + 'H716' : '');
		safetyLine.setAttribute('d', Number.isFinite(limit) ? 'M' + x(limit) + ' 36V276' : '');
		safetyLabel.setAttribute('x', Number.isFinite(limit) ? x(limit) - 5 : 716);
		safetyLabel.textContent = Number.isFinite(limit) ? _('%d°C 全速').format(limit) : '';
		const stopped = option('fan_stop')?.formvalue(sid) !== '0';
		let path = '';
		if (!message) {
			path = 'M62 ' + y(stopped ? 0 : p[0].s) + 'H' + x(p[0].t) + 'V' + y(p[0].s);
			for (let i = 1; i < 4; i++) path += 'L' + x(p[i].t) + ' ' + y(p[i].s);
			path += 'H' + x(limit) + 'V36H716';
		}
		line.setAttribute('d', path);
		area.setAttribute('d', path ? path + 'V276H62Z' : '');
		caption.textContent = message ? '' :
			(stopped ? _('低于 %d°C 后逐步停转，回升至 %d°C 起转。').format(p[0].t - 2, p[0].t)
				: _('低于第一个点时保持 %d%% 输出。').format(p[0].s)) +
			_('超过第四个点后保持 %d%%，达到 %d°C 强制全速。').format(p[3].s, limit);
		// The floor and full-speed fields validate against the curve; refresh their state.
		[ 'min_speed', 'emergency_temp' ].forEach(name => option(name)?.getUIElement(sid)?.triggerValidation());
	}
	function sync() {
		points().forEach((v, i) => {
			inputs.temp[i].value = Number.isFinite(v.t) ? v.t : '';
			inputs.speed[i].value = Number.isFinite(v.s) ? v.s : '';
		});
	}
	function store(name, value) {
		option(name).getUIElement(sid).setValue(String(value));
	}
	function move(i, t, s) {
		const p = points(), floor = number('min_speed'), limit = number('emergency_temp');
		if (curveError(p, floor, limit))
			return;
		t = Math.max(i ? p[i - 1].t + 2 : 20, Math.min(i < 3 ? p[i + 1].t - 2 : Math.min(95, limit - 2), Math.round(t)));
		s = Math.max(i ? p[i - 1].s : floor, Math.min(i < 3 ? p[i + 1].s : 100, Math.round(s)));
		store('temp' + (i + 1), t);
		store('speed' + (i + 1), s);
		sync();
		render();
	}

	for (let i = 0; i < 4; i++) {
		const group = svg('g', {}, handlesGroup);
		const circle = svg('circle', { 'r': 11, 'class': 'of-handle', 'tabindex': 0, 'role': 'slider',
			'aria-label': _('点 %d').format(i + 1), 'aria-valuemin': 20, 'aria-valuemax': 95 }, group);
		const label = svg('text', { 'text-anchor': 'middle', 'pointer-events': 'none', 'style': 'opacity:1' }, group);
		label.textContent = i + 1;
		handles.push({ group, circle, label });
		let drag = null;
		circle.addEventListener('pointerdown', ev => {
			drag = ev.pointerId;
			circle.setPointerCapture(ev.pointerId);
			circle.focus();
			ev.preventDefault();
		});
		circle.addEventListener('pointermove', ev => {
			if (drag !== ev.pointerId) return;
			const matrix = chart.getScreenCTM();
			if (!matrix) return;
			let pt = chart.createSVGPoint();
			pt.x = ev.clientX; pt.y = ev.clientY;
			pt = pt.matrixTransform(matrix.inverse());
			move(i, 20 + (pt.x - 62) / 8.175, (276 - pt.y) / 2.4);
		});
		const end = ev => {
			if (drag === ev.pointerId) {
				drag = null;
				if (circle.hasPointerCapture(ev.pointerId)) circle.releasePointerCapture(ev.pointerId);
			}
		};
		circle.addEventListener('pointerup', end);
		circle.addEventListener('pointercancel', end);
		circle.addEventListener('lostpointercapture', () => { drag = null; });
		circle.addEventListener('keydown', ev => {
			const p = points()[i], step = ev.shiftKey ? 5 : 1;
			if (ev.key === 'ArrowLeft') p.t -= step;
			else if (ev.key === 'ArrowRight') p.t += step;
			else if (ev.key === 'ArrowUp') p.s += step;
			else if (ev.key === 'ArrowDown') p.s -= step;
			else return;
			ev.preventDefault();
			move(i, p.t, p.s);
		});
		[ 'temp', 'speed' ].forEach(kind => inputs[kind][i].addEventListener('input', ev => {
			ev.stopPropagation();
			store(kind + (i + 1), ev.target.value);
			render();
		}));
	}

	return {
		node,
		init(root) {
			// Redraw when the floor, the full-speed temperature or the stop option changes.
			const watched = [ 'min_speed', 'emergency_temp', 'fan_stop' ].map(name => option(name)?.cbid(sid));
			const changed = ev => {
				const field = ev.target.closest?.('[data-field]')?.getAttribute('data-field');
				if (watched.includes(field)) render();
			};
			root.addEventListener('input', changed);
			root.addEventListener('widget-change', changed);
			sync();
			render();
		}
	};
}

return view.extend({
	load() {
		return uci.load('openfi');
	},

	// Exposed for tests.
	curveError,
	integer,
	legacyDefaults,
	readStatus,

	render() {
		const legacy = legacyDefaults();
		let m, s, o;

		m = new form.Map('openfi', _('OpenFi 6C'), _('CPU 温控风扇、实体开关和 5G 模块设置。修改后点击“保存并应用”生效。'));

		s = m.section(form.NamedSection, 'fan', 'fan', _('风扇控制'));
		s.addremove = false;
		s.tab('control', _('调速方式'));
		s.tab('curve', _('温控曲线'));

		o = s.taboption('control', form.ListValue, 'mode', _('控制模式'),
			_('手动输出与自动曲线分开保存，切换模式不会改动曲线。'));
		o.value('auto', _('自动 · 跟随 CPU 温度'));
		o.value('manual', _('手动 · 固定 PWM 输出'));
		o.default = 'auto';
		o.rmempty = false;

		const field = (title, lower, upper) => function(sid, value) {
			return integer(value, null, lower, upper) == null
				? _('%s：请输入 %d–%d 之间的整数。').format(title, lower, upper) : true;
		};
		const points = () => [1, 2, 3, 4].map(i => ({
			t: integer(m.lookupOption('temp' + i, 'fan')[0].formvalue('fan'), null, 20, 95),
			s: integer(m.lookupOption('speed' + i, 'fan')[0].formvalue('fan'), null, 0, 100)
		}));
		const floorValue = () => integer(m.lookupOption('min_speed', 'fan')[0].formvalue('fan'), null, 5, 100);
		const limitValue = () => integer(m.lookupOption('emergency_temp', 'fan')[0].formvalue('fan'), null, 60, 100);
		const curveCheck = () => curveError(points(), floorValue(), limitValue()) || true;

		o = s.taboption('control', form.Value, 'manual_speed', _('手动输出（%）'),
			_('0 为停转；非零输出不低于最低稳定输出。CPU 达到强制全速温度时仍会全速。'));
		o.depends('mode', 'manual');
		o.default = '55';
		o.rmempty = false;
		o.retain = true;
		o.validate = field(_('手动输出（%）'), 0, 100);

		o = s.taboption('control', form.Value, 'min_speed', _('最低稳定输出（%）'),
			_('同时约束手动输出和自动曲线，默认 5。风扇从停转起转时先全速助推 1 秒。'));
		o.default = legacy.floor;
		o.rmempty = false;
		o.validate = function(sid, value) {
			const range = field(_('最低稳定输出（%）'), 5, 100)(sid, value);
			return range === true ? curveCheck() : range;
		};

		const editor = curveEditor(m, 'fan');
		o = s.taboption('curve', form.DummyValue, '_curve');
		o.render = () => editor.node;

		for (let i = 1; i <= 4; i++) {
			for (const name of [ 'temp' + i, 'speed' + i ]) {
				o = s.taboption('curve', form.HiddenValue, name);
				o.default = legacy.points[name];
				o.rmempty = false;
				// The editor above shows these values; do not leave empty form rows behind.
				o.render = function() {
					return Promise.resolve(form.HiddenValue.prototype.render.apply(this, arguments)).then(node => {
						node.style.display = 'none';
						return node;
					});
				};
				// Hidden inputs have no validation of their own; check the whole curve on save.
				o.parse = function(sid) {
					const error = curveCheck();
					return error === true ? form.HiddenValue.prototype.parse.call(this, sid)
						: Promise.reject(new TypeError(error));
				};
			}
		}

		o = s.taboption('curve', form.Flag, 'fan_stop', _('低温停转'),
			_('低于第一个温度点 2°C 后逐步停转，回升到第一个点时起转。关闭后保持第一个点的输出。'));
		o.default = '1';
		o.rmempty = false;

		o = s.taboption('curve', form.Value, 'emergency_temp', _('强制全速温度（°C）'),
			_('自动和手动模式都生效，需比第四个温度点至少高 2°C。'));
		o.default = '85';
		o.rmempty = false;
		o.validate = function(sid, value) {
			const range = field(_('强制全速温度（°C）'), 60, 100)(sid, value);
			return range === true ? curveCheck() : range;
		};

		o = s.taboption('curve', form.Value, 'period', _('采样间隔（秒）'),
			_('升温立即响应，降温每次最多降 10 个百分点。读不到 CPU 温度时全速运行。'));
		o.default = '5';
		o.rmempty = false;
		o.validate = field(_('采样间隔（秒）'), 2, 30);

		s = m.section(form.NamedSection, 'switch', 'switch', _('拨动开关'),
			_('实体开关可接管 5G 模块电源或指示灯，未接管的项目在此设置。关闭 5G 电源会中断蜂窝网络。'));
		s.addremove = false;

		o = s.option(form.ListValue, 'func', _('实体开关用途'));
		o.value('0', _('不接管 · 由页面设置'));
		o.value('2', _('5G 模块电源'));
		o.value('3', _('指示灯'));
		o.default = '0';
		o.rmempty = false;
		o.cfgvalue = function(sid) {
			const value = uci.get('openfi', sid, 'func');
			return value === '2' || value === '3' ? value : '0';
		};

		// While the physical switch owns an output, keep the page setting for later.
		const softwareSwitch = (name, title, owner) => {
			const opt = s.option(form.ListValue, name, title);
			opt.value('0', _('关闭'));
			opt.value('1', _('开启'));
			[ '0', '2', '3' ].filter(v => v !== owner).forEach(v => opt.depends('func', v));
			opt.rmempty = false;
			opt.retain = true;
		};
		softwareSwitch('power', _('5G 模块电源'), '2');
		softwareSwitch('led', _('指示灯'), '3');

		s = m.section(form.NamedSection, 'reset', 'modem', _('高级硬件设置'));
		s.addremove = false;
		o = s.option(form.Flag, 'state', _('反转模块复位引脚'), _('用于适配不同模块的复位电平，通常保持默认。'));
		o.default = '1';
		o.rmempty = false;

		const status = statusSection();
		return m.render().then(node => {
			node.insertBefore(status.node, node.querySelector('.cbi-section'));
			editor.init(node);
			status.refresh();
			poll.add(status.refresh, 3);
			return node;
		});
	}
});
