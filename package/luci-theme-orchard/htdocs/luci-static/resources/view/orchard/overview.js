'use strict';
'require view';
'require rpc';
'require network';
'require poll';
'require ui';
'require orchard-cpu as orchardCpu';

var board = rpc.declare({ object: 'system', method: 'board', expect: { '': {} } });
var info = rpc.declare({ object: 'system', method: 'info', expect: { '': {} } });
var leases = rpc.declare({ object: 'luci-rpc', method: 'getDHCPLeases', expect: { '': {} } });

return view.extend({
 handleSave: null,
 handleSaveApply: null,
 handleReset: null,
 previous: null,
 previousCpu: null,
 history: [],
 sample: function () {
  return Promise.all([board(), info(), leases(), network.getWANNetworks(), orchardCpu.read().catch(function () { return null; })]).then(L.bind(function (result) {
   var now = performance.now();
   var up = result[3].filter(function (net) { return net.isUp(); });
   var devices = up.map(function (net) { return net.getL3Device(); }).filter(Boolean);
   devices = devices.filter(function (device, index, all) { return all.findIndex(function (other) { return other.getName() === device.getName(); }) === index; });
   var rx = devices.reduce(function (n, dev) { return n + (dev.getRXBytes() || 0); }, 0);
   var tx = devices.reduce(function (n, dev) { return n + (dev.getTXBytes() || 0); }, 0);
   var signature = devices.map(function (dev) { return dev.getName(); }).sort().join(',');
   var previous = this.previous;
   var cpu = orchardCpu.usage(this.previousCpu, result[4]);
   this.previousCpu = result[4];
   var elapsed = previous ? (now - previous.time) / 1000 : 0;
   var valid = devices.length > 0 && previous && signature === previous.signature && elapsed > 0 && rx >= previous.rx && tx >= previous.tx && result[1].uptime >= previous.uptime;
   var down = valid ? (rx - previous.rx) / elapsed : null;
   var upload = valid ? (tx - previous.tx) / elapsed : null;
   this.previous = { time: now, rx: rx, tx: tx, signature: signature, uptime: result[1].uptime };
   if (valid) this.history.push({ down: down, up: upload });
   else this.history = [];
   this.history = this.history.slice(-36);
   var liveLeases = (result[2].dhcp_leases || []).filter(function (lease) { return lease.expires === false || Number(lease.expires) > 0; });
   return {
    board: result[0], info: result[1], cpu: cpu, leases: liveLeases,
    wan: up.length ? { name: up[0].getName(), protocol: up[0].getI18n(), ip: (up[0].getIPAddrs() || [])[0], up: true } : { up: false },
    traffic: { down: down, up: upload, history: this.history.slice(), interval: 5 },
    links: { interfaces: L.url('admin/network/network'), wireless: L.url('admin/network/wireless'), system: L.url('admin/system/system'), leases: L.url('admin/network/dhcp'), status: L.url('admin/status/overview') }
   };
  }, this));
 },
 load: function () {
  if (!document.body.classList.contains('orchard')) return Promise.resolve({ inactive: true });
  return Promise.all([
   this.sample(),
   window.OrchardOverview ? Promise.resolve() : new Promise(function (resolve, reject) {
    var script = document.createElement('script');
    script.src = L.env.media + '/overview-ui.js?v=' + encodeURIComponent(document.body.dataset.orchardVersion || '');
    script.onload = resolve;
    script.onerror = function () { reject(new Error('Unable to load Orchard overview')); };
    document.head.appendChild(script);
   })
  ]).then(function (result) { return result[0]; });
 },
 render: function (data) {
  if (data.inactive) return E('div', {}, [E('h2', {}, 'Orchard'), E('p', {}, _('Select the Orchard theme in System settings to use this overview.')), E('a', { href: L.url('admin/system/system') }, _('System'))]);
  var container = E('div', { id: 'orchard-overview' });
  var update = L.bind(function () {
   return network.flushCache().then(L.bind(this.sample, this)).then(function (fresh) {
    window.OrchardOverview.render(container, fresh);
   }).catch(function () {
    var time = container.querySelector('.o-updated');
    if (time) { time.textContent = _('Unable to refresh data'); time.classList.add('o-stale'); }
   });
  }, this);
  window.OrchardOverview.render(container, data);
  var mounted = false;
  var poller = function () {
   if (!document.contains(container)) { if (mounted) poll.remove(poller); return; }
   mounted = true;
   return update();
  };
  poll.add(poller, 5);
  return container;
 }
});
