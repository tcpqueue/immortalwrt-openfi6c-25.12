// End-to-end check of the OpenFi settings view on a LuCI test machine.
// Usage: LUCI_URL=http://127.0.0.1:18080 LUCI_PASS=... VM_SSH=/path/to/ssh-wrapper node tests/openfi6c/luci-vm.mjs
// VM_SSH runs a shell command on the router (stdin closed), e.g. a wrapper around `ssh root@router`.
// The script rewrites /etc/config/openfi on that machine; only use a disposable test system.
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { createRequire } from 'node:module';

const require = createRequire(process.env.PLAYWRIGHT_FROM || import.meta.url);
const { chromium } = require('playwright');
const base = (process.env.LUCI_URL || 'http://192.168.1.1') + '/cgi-bin/luci';
const sh = cmd => execFileSync(process.env.VM_SSH, [cmd], { input: '', encoding: 'utf8' }).trim();
const get = opt => sh(`uci -q get openfi.${opt} || true`);

const CONFIG = `config fan 'fan'
	option temp1 '40'
	option speed1 '6'
	option temp2 '50'
	option speed2 '25'
	option temp3 '60'
	option speed3 '50'
	option temp4 '75'
	option speed4 '80'
	option emergency_temp '85'
	option fan_stop '1'
	option period '5'
	option mode 'auto'
	option min_speed '6'
	option manual_speed '55'

config switch 'switch'
	option func '0'
	option power '1'
	option led '1'

config modem 'reset'
	option state '1'
`;
const LEGACY = `config fan 'fan'
	option level '1'
	option cpu_temp_low '50'
	option cpu_temp_high '68'

config switch 'switch'
	option func '0'
	option power '1'
	option led '1'

config modem 'reset'
	option state '1'
`;
const curve = () => ['temp1', 'speed1', 'temp2', 'speed2', 'temp3', 'speed3', 'temp4', 'speed4'].map(k => get('fan.' + k)).join(',');

function writeConfig(text) {
	sh(`printf '%s' '${text.replace(/'/g, "'\\''")}' > /etc/config/openfi`);
}

const browser = await chromium.launch();
const page = await browser.newPage({ locale: 'zh-CN' });
const errors = [];
page.on('pageerror', e => errors.push(String(e)));

async function open() {
	await page.goto(base + '/admin/openfi/openfi6');
	if (await page.locator('input[name="luci_password"]').count()) {
		await page.fill('input[name="luci_password"]', process.env.LUCI_PASS || '');
		await Promise.all([page.waitForNavigation(), page.getByRole('button', { name: /登录|Log in/ }).first().click()]);
		await page.goto(base + '/admin/openfi/openfi6');
	}
	await page.waitForSelector('#of-editor', { state: 'attached' });
	errors.length = 0;
}
const field = name => page.locator(`[data-field="cbid.openfi.${name}"]`);
async function tab(name) { await page.locator('.cbi-tabmenu a', { hasText: name }).first().click(); }
async function saveApply() {
	await page.locator('.cbi-page-actions .cbi-button-apply').first().click();
	// LuCI reports a finished apply with a short status message.
	const reloaded = page.waitForEvent('load', { timeout: 30000 }).catch(() => {});
	await page.waitForFunction(() => /配置更改已应用|Configuration changes applied/.test(document.body.innerText), null, { timeout: 90000 });
	// LuCI reloads the page after a successful apply.
	await reloaded;
}

try {
	writeConfig(CONFIG);
	await open();
	const original = curve();

	// 1. Manual 100% and back to automatic keep both settings.
	for (let i = 0; i < 2; i++) {
		await tab('调速方式');
		await field('fan.mode').locator('select').selectOption('manual');
		await field('fan.manual_speed').locator('input').fill('100');
		await saveApply();
		assert.equal(get('fan.mode'), 'manual');
		assert.equal(get('fan.manual_speed'), '100');
		await open();
		await tab('调速方式');
		await field('fan.mode').locator('select').selectOption('auto');
		await saveApply();
		assert.equal(get('fan.mode'), 'auto');
		assert.equal(get('fan.manual_speed'), '100', 'hidden manual output is retained');
		assert.equal(curve(), original, 'mode round trips keep every curve point');
		await open();
	}

	// 2. Editing a point in the grid is saved.
	await tab('温控曲线');
	await page.locator('#of-speed2').fill('30');
	await saveApply();
	assert.equal(get('fan.speed2'), '30');
	const edited = curve();

	// 3. A floor above the curve is rejected without touching the curve.
	await open();
	await tab('调速方式');
	await field('fan.min_speed').locator('input').fill('100');
	assert.notEqual(await page.locator('#of-curve-error').textContent(), '', 'the editor reports the conflict');
	assert.equal(await field('fan.min_speed').locator('input.cbi-input-invalid').count(), 1, 'the floor field is marked invalid');
	await page.locator('.cbi-page-actions .cbi-button-save').first().click();
	await page.waitForTimeout(1500);
	assert.equal(get('fan.min_speed'), '6', 'conflicting floor is not saved');
	assert.equal(curve(), edited);

	// 4. A physically switched output keeps its page setting.
	await open();
	await field('switch.power').locator('select').selectOption('0');
	await saveApply();
	assert.equal(get('switch.power'), '0');
	await open();
	await field('switch.func').locator('select').selectOption('2');
	assert.equal(await field('switch.power').isHidden(), true, 'the switch owns the modem power');
	await saveApply();
	assert.equal(get('switch.func'), '2');
	assert.equal(get('switch.power'), '0', 'the hidden page setting is kept');
	await open();
	await field('switch.func').locator('select').selectOption('3');
	await saveApply();
	assert.equal(get('switch.power'), '0', 'switching the owner does not reset the page setting');

	// 5. A configuration from the three-level firmware gets a derived floor and curve.
	writeConfig(LEGACY);
	await open();
	await tab('调速方式');
	assert.equal(await field('fan.min_speed').locator('input').inputValue(), '55');
	await tab('温控曲线');
	assert.equal(await page.locator('#of-temp1').inputValue(), '50');
	assert.equal(await page.locator('#of-temp4').inputValue(), '68');
	assert.equal(await page.locator('#of-speed4').inputValue(), '100');
	await saveApply();
	assert.equal(get('fan.min_speed'), '55');
	assert.equal(curve(), '50,55,56,70,62,85,68,100');

	assert.deepEqual(errors, []);
	console.log('OpenFi view: mode round trips, point edits, floor conflicts, switch ownership and legacy defaults verified.');
} finally {
	writeConfig(CONFIG);
	await browser.close();
}
