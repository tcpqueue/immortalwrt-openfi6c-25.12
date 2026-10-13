'use strict';
'require baseclass';
'require rpc';
'require poll';

var readStat = rpc.declare({ object: 'file', method: 'read', params: ['path'], expect: { data: '' } });

return baseclass.extend({
 parse: function (text) {
  var line = String(text).split('\n').find(function (row) { return /^cpu\s/.test(row); });
  if (!line) return null;
  var fields = line.trim().split(/\s+/).slice(1, 9).map(Number);
  if (fields.length < 4 || fields.some(function (n) { return !Number.isFinite(n) || n < 0; })) return null;
  // guest and guest_nice are already included in user and nice, respectively.
  return { total: fields.reduce(function (sum, n) { return sum + n; }, 0), idle: fields[3] + (fields[4] || 0) };
 },
 usage: function (previous, current) {
  if (!previous || !current) return null;
  var total = current.total - previous.total, idle = current.idle - previous.idle;
  if (total <= 0 || idle < 0 || idle > total) return null;
  return Math.max(0, Math.min(100, (total - idle) / total * 100));
 },
 read: function () { return readStat('/proc/stat').then(L.bind(this.parse, this)); },
 monitor: function () {
  if (this.monitoring) return;
  this.monitoring = true;
  var previous = null, value = null, failed = false;
  var zh = (document.documentElement.lang || '').startsWith('zh');
  var label = zh ? 'CPU 使用率（%）' : 'CPU usage (%)';
  var markOverview = function () {
   var view = document.getElementById('view');
   if (!view) return;
   var names = [[_('System'), 'system'], [_('Memory'), 'memory'], [_('Storage'), 'storage']];
   var sections = Array.from(view.children).filter(function (element) { return element.classList.contains('cbi-section'); });
   var matches = names.map(function (entry) {
    return sections.find(function (section) { return section.querySelector(':scope > h3')?.textContent.trim() === entry[0] && section.querySelector('table'); });
   });
   // Optional or failed includes retain the regular full-width layout.
   matches.forEach(function (section, index) { if (section) section.classList.add('o-status-' + names[index][1]); });
   if (matches.some(function (section) { return !section; })) return;
   view.classList.add('o-status-grid');
  };
  var render = function () {
   markOverview();
   var table = document.querySelector('#view > .o-status-system table');
   if (!table) return;
   var row = Array.from(table.rows).find(function (item) { return /^CPU\s*(使用率|usage)/i.test(item.cells[0]?.textContent || ''); });
   if (!row) {
    row = E('tr', { 'class': 'tr', 'data-orchard-cpu': '' }, [E('td', { 'class': 'td left', width: '33%' }, label), E('td', { 'class': 'td left' })]);
    table.appendChild(row);
   }
   var text = value == null ? (failed ? (zh ? '读取失败' : 'Unavailable') : (zh ? '正在采样…' : 'Collecting data…')) : value.toFixed(1) + '%';
   if (row.cells[1].textContent !== text) row.cells[1].textContent = text;
  };
  var root = document.getElementById('maincontent');
  if (!root) return;
  var observer = new MutationObserver(render);
  observer.observe(root, { childList: true, subtree: true });
  render();
  poll.add(L.bind(function () {
   return this.read().then(L.bind(function (current) {
    value = this.usage(previous, current);
    previous = current;
    failed = current == null;
    render();
   }, this)).catch(function () { previous = null; value = null; failed = true; render(); });
  }, this), 5);
 }
});
