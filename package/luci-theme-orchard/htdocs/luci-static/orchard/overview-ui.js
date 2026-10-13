/* SPDX-License-Identifier: Apache-2.0 */
(function () {
 'use strict';
 var zh = (document.documentElement.lang || '').startsWith('zh');
 function text(en, cn) { return zh ? cn : en; }
 function el(tag, cls, content) {
  var node = document.createElement(tag);
  if (cls) node.className = cls;
  if (content != null) {
   if (Array.isArray(content)) content.forEach(function (item) { if (item != null) node.append(item); });
   else node.append(content);
  }
  return node;
 }
 function icon(name) { return window.Orchard.icon(name); }
 function link(label, url, cls) { var node = el('a', cls, label); node.href = url; return node; }
 function pill(label, online) { return el('span', 'o-pill' + (online ? ' o-pill-green' : ''), [el('i', 'o-dot'), label]); }
 function bytes(n) { return n >= 1073741824 ? (n / 1073741824).toFixed(1) + ' GB' : Math.round(n / 1048576) + ' MB'; }
 function rate(n) {
  if (n == null) return '—';
  var bits = n * 8;
  return bits >= 1e6 ? (bits / 1e6).toFixed(1) + ' Mbps' : Math.round(bits / 1000) + ' Kbps';
 }
 function heading(label, aside) { return el('div', 'o-section-heading', [el('h3', '', label), aside]); }
 function chart(series) {
  var holder = el('div', 'o-chart');
  var ns = 'http://www.w3.org/2000/svg';
  var svg = document.createElementNS(ns, 'svg');
  svg.setAttribute('viewBox', '0 0 700 128');
  svg.setAttribute('preserveAspectRatio', 'none');
  svg.setAttribute('aria-hidden', 'true');
  var max = Math.max(1, ...series.map(function (point) { return Math.max(point.down, point.up); })) * 1.2;
  for (var y = 10; y <= 110; y += 33.33) {
   var grid = document.createElementNS(ns, 'path');
   grid.setAttribute('d', 'M0 ' + y + ' H700'); grid.setAttribute('class', 'o-chart-grid'); svg.append(grid);
  }
  ['down', 'up'].forEach(function (key) {
   if (series.length < 2) return;
   var firstX = (36 - series.length) / 35 * 700;
   var points = series.map(function (point, index) { return ((36 - series.length + index) / 35 * 700).toFixed(2) + ',' + (112 - point[key] / max * 100).toFixed(2); });
   if (key === 'down') {
    var area = document.createElementNS(ns, 'path');
    area.setAttribute('d', 'M' + firstX + ' 112 L' + points.join(' L') + ' L700 112 Z'); area.setAttribute('class', 'o-chart-area'); svg.append(area);
   }
   var path = document.createElementNS(ns, 'polyline'); path.setAttribute('points', points.join(' ')); path.setAttribute('class', 'o-chart-line o-chart-' + key); svg.append(path);
  });
  holder.append(svg);
  if (series.length < 2) holder.append(el('span', 'o-chart-wait', text('Collecting traffic samples…', '正在采集流量数据…')));
  return holder;
 }
 function stat(label, value, note, name, color) {
  return el('div', 'o-stat o-card', [el('div', 'o-stat-heading', [el('span', 'o-stat-icon ' + color, icon(name)), el('span', '', label)]), el('div', 'o-stat-value', value), el('div', 'o-stat-note', note)]);
 }
 function row(label, value) { return el('div', 'o-info-row', [el('span', '', label), el('span', '', value == null ? '—' : String(value))]); }
 function render(container, data) {
  var board = data.board || {}, info = data.info || {}, traffic = data.traffic || {}, wan = data.wan || {}, leases = data.leases || [], urls = data.links || {};
  var memory = info.memory || {}, total = memory.total || 0;
  var available = memory.available == null ? (memory.free || 0) + (memory.buffered || 0) + (memory.cached || 0) : memory.available;
  var used = total ? Math.max(0, Math.min(total, total - available)) : 0;
  var percent = total ? Math.round(used / total * 100) : null;
  var uptime = info.uptime || 0, days = Math.floor(uptime / 86400), hours = Math.floor(uptime % 86400 / 3600), minutes = Math.floor(uptime % 3600 / 60);
  var load = Array.isArray(info.load) ? (info.load[0] / 65535).toFixed(2) : '—';
  var top = el('div', 'o-page-heading', [el('div', '', [el('div', 'o-eyebrow', text('NETWORK STATUS', '网络状态')), el('h2', '', text('Overview', '总览')), el('p', '', text('Connection and system status for this router.', '查看这台路由器的连接与运行状态。'))]), el('div', 'o-updated', [el('i', 'o-dot'), data.demo ? text('Offline preview · sample data', '离线预览 · 示例数据') : text('Refreshes every 5 seconds', '每 5 秒自动刷新')])]);
  var status = el('div', 'o-network-status', [el('div', 'o-network-orb', icon('network')), el('div', '', [el('h3', '', wan.up ? text('WAN interface is up', 'WAN 接口已连接') : text('No active WAN interface', '暂无活动 WAN 接口')), el('p', '', wan.up ? text('Interface link is up; internet reachability is not tested.', '接口链路正常；此处不检测互联网连通性。') : text('Check the uplink in network settings.', '可前往网络设置检查上行接口。'))]), pill(wan.up ? text('Connected', '已连接') : text('Disconnected', '未连接'), wan.up)]);
  var topology = el('div', 'o-topology', [el('div', 'o-topology-node', [icon('network'), el('strong', '', text('Upstream', '上行网络')), el('small', '', wan.protocol || '—')]), el('div', 'o-topology-line', el('span', '', wan.ip || text('No address', '无地址'))), el('div', 'o-topology-node', [icon('router'), el('strong', '', board.hostname || 'OpenWrt'), el('small', '', text('Router', '路由器'))]), el('div', 'o-topology-line'), el('div', 'o-topology-node', [icon('device'), el('strong', '', leases.length + text(' leases', ' 个租约')), el('small', '', text('DHCP clients', 'DHCP 客户端'))])]);
  var networkCard = el('section', 'o-network-card o-card', [status, topology, el('div', 'o-card-foot', [el('span', '', text('Interfaces, protocols and addresses', '接口、协议与地址')), link(text('Network settings', '网络设置'), urls.interfaces || '#network', 'o-text-link')])]);
  var stats = el('div', 'o-stats', [stat(text('Memory', '内存使用'), percent == null ? '—' : percent + '%', total ? bytes(used) + ' / ' + bytes(total) : '—', 'chip', 'o-blue'), stat(text('CPU usage', 'CPU 使用率'), data.cpu == null ? '—' : data.cpu.toFixed(1) + '%', text('Average across all cores · 5 seconds', '全部核心平均 · 5 秒采样'), 'system', 'o-purple'), stat(text('Uptime', '运行时间'), days ? days + text(' days', ' 天') : hours + text(' hours', ' 小时'), (days ? hours + text(' hours · ', ' 小时 · ') : '') + minutes + text(' minutes', ' 分钟'), 'clock', 'o-orange'), stat(text('DHCP leases', 'DHCP 租约'), String(leases.length), text('Active leases ≠ online devices', '有效租约 ≠ 在线设备'), 'device', 'o-green')]);
  var trafficCard = el('section', 'o-card o-traffic-card', [heading(text('Network activity', '网络活动'), el('span', 'o-caption', text('WAN · last 3 minutes', 'WAN · 最近 3 分钟'))), el('div', 'o-traffic-rates', [el('div', '', [el('span', 'o-rate-label', [icon('down'), text('Download', '下载')]), el('strong', '', rate(traffic.down))]), el('div', '', [el('span', 'o-rate-label o-rate-upload', [icon('up'), text('Upload', '上传')]), el('strong', '', rate(traffic.up))])]), chart(traffic.history || []), el('div', 'o-chart-axis', [el('span', '', text('3 min ago', '3 分钟前')), el('span', '', text('Now', '现在'))])]);
  var systemCard = el('section', 'o-card o-system-card', [heading(text('About this router', '关于这台路由器'), link(text('Details', '详情'), urls.system || '#system', 'o-text-link')), el('div', 'o-router-identity', [el('div', 'o-router-illustration', icon('router')), el('div', '', [el('strong', '', board.model || 'OpenWrt'), el('span', '', board.release?.description || '—')])]), row(text('Hostname', '主机名'), board.hostname), row(text('Kernel', '内核版本'), board.kernel), row(text('Architecture', '架构'), board.release?.target || board.system), row(text('Load average (1 min)', '平均负载（1 分钟）'), load)]);
  var clients = el('section', 'o-card o-clients-card', [heading(text('DHCP clients', 'DHCP 客户端'), link(text('Manage', '管理'), urls.leases || '#clients', 'o-text-link'))]);
  var table = el('table', 'table o-clients-table');
  var head = el('thead', '', el('tr', '', [el('th', '', text('Device', '设备')), el('th', '', text('IP address', 'IP 地址')), el('th', '', text('MAC address', 'MAC 地址')), el('th', '', text('Lease remaining', '租约剩余'))]));
  var body = el('tbody');
  leases.slice(0, 6).forEach(function (lease) {
   var expire = lease.expires === false ? text('Unlimited', '无限') : Math.max(0, Math.floor(lease.expires / 3600)) + text(' h ', ' 小时 ') + Math.max(0, Math.floor(lease.expires % 3600 / 60)) + text(' min', ' 分钟');
   body.append(el('tr', '', [el('td', '', el('div', 'o-client-name', [el('span', 'o-client-icon', icon('device')), el('strong', '', lease.hostname || text('Unnamed device', '未命名设备'))])), el('td', 'o-mono', lease.ipaddr || '—'), el('td', 'o-mono o-mac', lease.macaddr || '—'), el('td', '', expire)]));
  });
  if (!leases.length) { var cell = el('td', 'o-empty', text('No active DHCP leases', '暂无有效 DHCP 租约')); cell.colSpan = 4; body.append(el('tr', '', cell)); }
  table.append(head, body); clients.append(el('div', 'o-table-scroll', table));
  if (leases.length > 6) clients.append(el('div', 'o-card-foot', text('Showing 6 of ', '显示前 6 个，共 ') + leases.length));
  container.replaceChildren(top, networkCard, stats, el('div', 'o-middle-grid', [trafficCard, systemCard]), clients);
 }
 window.OrchardOverview = { render: render };
})();
