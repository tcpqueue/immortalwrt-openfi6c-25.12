'use strict';
'require baseclass';
'require ui';
'require orchard-cpu as orchardCpu';

return baseclass.extend({
 __init__: function () {
  if (document.querySelector('input[name="luci_password"]')) return;
  if (L.env.dispatchpath.join('/') === 'admin/status/overview') orchardCpu.monitor();
  ui.menu.load().then(L.bind(this.render, this)).catch(function (error) {
   ui.addNotification(null, E('p', _('Unable to load the navigation menu: ') + error.message));
  });
 },
 render: function (tree) {
  var modes = ui.menu.getChildren(tree);
  var mode = modes.find(function (child) { return child.name === L.env.requestpath[0]; }) || modes[0];
  if (!mode) return;
  var container = document.getElementById('orchard-menu');
  if (!container) return;
  container.replaceChildren();
  ui.menu.getChildren(mode).forEach(function (group) {
   var children = ui.menu.getChildren(group);
   var activeGroup = L.env.dispatchpath[1] === group.name;
   var graphic = window.Orchard.icon(group.name);
   graphic.classList.add('orchard-menu-icon', 'orchard-menu-icon-' + group.name);
   if (!children.length) {
    container.appendChild(E('a', { 'class': 'orchard-menu-single', href: L.url(mode.name, group.name) }, [graphic, E('span', _(group.title))]));
    return;
   }
   var details = E('details', { 'class': 'orchard-menu-group' });
   details.open = activeGroup;
   details.appendChild(E('summary', {}, [graphic, E('span', _(group.title)), window.Orchard.icon('arrow')]));
   var list = E('ul');
   children = children.slice().sort(function (a, b) { return a.name === 'orchard' ? -1 : b.name === 'orchard' ? 1 : 0; });
   children.forEach(function (child) {
    var active = activeGroup && L.env.dispatchpath[2] === child.name;
    var attrs = { href: L.url(mode.name, group.name, child.name), 'class': 'orchard-menu-link' + (active ? ' active' : '') };
    if (active) attrs['aria-current'] = 'page';
    list.appendChild(E('li', {}, E('a', attrs, _(child.title))));
    if (active) {
     var title = document.getElementById('orchard-location-title');
     if (title) title.textContent = _(child.title);
    }
   });
   details.appendChild(list);
   container.appendChild(details);
  });
  var modemenu = document.getElementById('modemenu');
  if (modemenu && modes.length > 1) {
   modemenu.replaceChildren();
   modes.forEach(function (child) { modemenu.appendChild(E('li', { 'class': child.name === mode.name ? 'active' : '' }, E('a', { href: L.url(child.name) }, _(child.title)))); });
   modemenu.style.display = '';
  }
  var node = tree, path = [];
  for (var i = 0; i < 3 && node; i++) {
   var name = L.env.dispatchpath[i];
   node = node.children?.[name];
   if (name) path.push(name);
  }
  var tabs = document.getElementById('tabmenu');
  if (tabs) tabs.replaceChildren();
  if (node) this.renderTabs(node, path, 3);
 },
 renderTabs: function (node, path, depth) {
  var children = ui.menu.getChildren(node);
  if (!children.length) return;
  var tabs = document.getElementById('tabmenu');
  if (!tabs) return;
  var list = E('ul', { 'class': 'tabs' });
  var active;
  children.forEach(function (child) {
   var selected = L.env.dispatchpath[depth] === child.name;
   var attrs = { href: L.url.apply(L, path.concat(child.name)) };
   if (selected) { active = child; attrs['aria-current'] = 'page'; }
   list.appendChild(E('li', { 'class': 'tabmenu-item-' + child.name + (selected ? ' active' : '') }, E('a', attrs, _(child.title))));
  });
  tabs.appendChild(list);
  tabs.style.display = '';
  if (active) this.renderTabs(active, path.concat(active.name), depth + 1);
 }
});
