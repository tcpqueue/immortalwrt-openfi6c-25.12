// Run with Node.js and Playwright (Chromium) available in the test environment.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { chromium } = require('playwright');

const views = 'package/mtk/applications/luci-app-openfi/luasrc/view/openfi/';
const original = { temp1:'40', speed1:'5', temp2:'50', speed2:'25',
  temp3:'60', speed3:'50', temp4:'75', speed4:'80' };

function range(name, value, minimum) {
  return '<div id="cbi-openfi-fan-' + name + '" data-index="1">' +
    fs.readFileSync(views + 'range.htm', 'utf8')
      .replace(/<%[\s\S]*?%>/g, tag => {
        if (tag === '<%=minimum%>') return minimum;
        if (tag === '<%=v%>') return value;
        if (tag === '<%=cbid%>') return 'cbid.openfi.fan.' + name;
        return '';
      }) + '</div>';
}

function markup(saved = original, mode = 'auto', manual = '55') {
  let curve = fs.readFileSync(views + 'curve.htm', 'utf8');
  curve = curve.replace(/<% for i=1,4 do %>([\s\S]*?)<% end %>/,
    (_, card) => [1,2,3,4].map(i => card.replaceAll('<%=i%>', i)).join(''));
  curve = curve.replace(/<%[\s\S]*?%>/g, '');
  const hidden = Object.entries(saved).map(([key, value]) =>
    `<input type="hidden" id="cbid.openfi.fan.${key}" name="${key}" value="${value}">`).join('');
  return `<form id="cbi-openfi"><div id="fan-fields">
    <select id="cbid.openfi.fan.mode"><option value="auto">Auto</option><option value="manual">Manual</option></select>
    ${range('manual_speed', manual, 0)}${range('min_speed', '5', 5)}
    <input id="cbid.openfi.fan.emergency_temp" value="85">
    <input type="checkbox" id="cbid.openfi.fan.fan_stop" checked>
    ${hidden}${curve}</div></form>
    <script>
    document.getElementById('cbid.openfi.fan.mode').value=${JSON.stringify(mode)};
    var L={require:()=>Promise.resolve({}),dom:{matches:(node, selector)=>node.nodeType===1 && node.matches(selector)}};
    </script>
    <script>${fs.readFileSync('feeds/luci/modules/luci-base/htdocs/luci-static/resources/cbi.js', 'utf8')}</script>
    <script>
    cbi_d_add('cbi-openfi-fan-manual_speed', {'cbid.openfi.fan.mode':'manual'}, 1);
    document.getElementById('cbid.openfi.fan.mode').addEventListener('change', cbi_d_update);
    cbi_d_update();
    </script>`;
}

(async () => {
  const browser = await chromium.launch({headless:true});
  try {
    const page = await browser.newPage();
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    const read = () => page.evaluate(() => Object.fromEntries(
      [...document.querySelectorAll('input[type=hidden][name]')].map(input => [input.name, input.value])));
    const mode = page.locator('[id="cbid.openfi.fan.mode"]');
    await page.setContent(markup());
    assert.deepEqual(await read(), original);
    for (let i=0; i<3; i++) {
      await mode.selectOption('manual');
      await page.locator('[id="cbid.openfi.fan.manual_speed"]').fill('100');
      assert.deepEqual(await read(), original, 'Manual 100% must not rewrite the automatic curve');
      await mode.selectOption('auto');
      assert.deepEqual(await read(), original, 'Switching back to auto must preserve every point');
    }
    console.log('Mode round trips with manual 100% preserve the automatic curve.');
    await page.locator('#of-speed2').fill('30');
    const edited = {...original, speed2:'30'};
    await mode.selectOption('manual');
    await mode.selectOption('auto');
    assert.deepEqual(await read(), edited, 'Unsaved curve edits must survive mode switches');
    const saved = await read();
    await page.goto('about:blank');
    await page.setContent(markup(saved, 'manual', '100'));
    await mode.selectOption('auto');
    assert.deepEqual(await read(), edited, 'Saved curve values must survive page reconstruction');
    await page.locator('[id="cbid.openfi.fan.min_speed"]').fill('100');
    assert.deepEqual(await read(), edited, 'Raising the floor must not overwrite curve points');
    assert.notEqual(await page.locator('#of-curve-error').textContent(), '', 'Conflicting settings need an explicit error');
    await page.locator('[id="cbid.openfi.fan.min_speed"]').fill('5');
    assert.deepEqual(await read(), edited, 'Lowering the floor must restore the same curve');
    assert.equal(await page.locator('#of-curve-error').textContent(), '');
    assert.deepEqual(errors, []);
    console.log('Curve edits, saved page state and minimum-output changes preserve all four points.');
  } finally {
    await browser.close();
  }
})().catch(error => {console.error(error); process.exitCode=1;});
