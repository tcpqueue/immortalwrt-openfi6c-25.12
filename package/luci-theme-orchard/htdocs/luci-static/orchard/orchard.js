/* SPDX-License-Identifier: Apache-2.0 */
(function () {
 'use strict';
 var root = document.documentElement;
 var query = window.matchMedia('(prefers-color-scheme: dark)');
 var mobileQuery = window.matchMedia('(max-width: 760px)');
 var appearance = 'system';
 try { appearance = localStorage.getItem('orchard.appearance') || 'system'; } catch (_) {}
 if (!['system', 'light', 'dark'].includes(appearance)) appearance = 'system';
 function applyAppearance(value) {
  appearance = value;
  root.dataset.darkmode = String(value === 'dark' || (value === 'system' && query.matches));
  root.dataset.appearance = value;
  document.querySelectorAll('[data-appearance]').forEach(function (button) {
   button.setAttribute('aria-pressed', String(button.dataset.appearance === value));
  });
 }
 applyAppearance(appearance);
 if (query.addEventListener) query.addEventListener('change', function () { applyAppearance(appearance); });
 else if (query.addListener) query.addListener(function () { applyAppearance(appearance); });
 var paths = {
  sun: '<circle cx="12" cy="12" r="4"/><path d="M12 2v2m0 16v2M2 12h2m16 0h2M5 5l1.5 1.5m11 11L19 19M5 19l1.5-1.5m11-11L19 5"/>',
  moon: '<path d="M20.5 13.5A8.5 8.5 0 0 1 10.5 3a8.5 8.5 0 1 0 10 10.5Z"/>',
  display: '<rect x="3" y="4" width="18" height="13" rx="2"/><path d="M8 21h8m-4-4v4"/>',
  sidebar: '<rect x="3" y="4" width="18" height="16" rx="3"/><path d="M9 4v16"/>',
  status: '<rect x="3" y="3" width="7" height="7" rx="2"/><rect x="14" y="3" width="7" height="7" rx="2"/><rect x="3" y="14" width="7" height="7" rx="2"/><rect x="14" y="14" width="7" height="7" rx="2"/>',
  network: '<rect x="8" y="2" width="8" height="6" rx="1.5"/><rect x="2" y="16" width="7" height="6" rx="1.5"/><rect x="15" y="16" width="7" height="6" rx="1.5"/><path d="M12 8v4M5.5 16v-4h13v4"/>',
  wifi: '<path d="M2 8a16 16 0 0 1 20 0M5 12a11 11 0 0 1 14 0M8.5 16a5.5 5.5 0 0 1 7 0"/><circle cx="12" cy="20" r=".7" fill="currentColor"/>',
  system: '<path d="m10 3-1 3-3 1-3 3 2 2-1 3 3 3 3-1 2 2 3-1 1-3 3-1 1-4-3-2-1-3-4-1-2 2Z"/><circle cx="12" cy="12" r="3"/>',
  services: '<path d="M12 3v18M3 12h18M5.5 5.5l13 13m-13 0 13-13"/><circle cx="12" cy="12" r="8"/>',
  openfi: '<circle cx="12" cy="12" r="1.8"/><path d="M12 10.2C11 6 12 3 15 3.5s2.5 4.5-1.4 7.2M13.8 12c4.2-1 7.2 0 6.7 3s-4.5 2.5-7.2-1.4M12 13.8c1 4.2 0 7.2-3 6.7s-2.5-4.5 1.4-7.2M10.2 12C6 13 3 12 3.5 9s4.5-2.5 7.2 1.4"/>',
  logout: '<path d="M9 4H4v16h5m5-12 4 4-4 4m-7-4h11"/>',
  lock: '<rect x="5" y="10" width="14" height="11" rx="3"/><path d="M8 10V7a4 4 0 0 1 8 0v3m-4 5v2"/>',
  down: '<path d="M12 3v16m-6-6 6 6 6-6"/>',
  up: '<path d="M12 21V5m-6 6 6-6 6 6"/>',
  arrow: '<path d="m9 5 7 7-7 7"/>',
  check: '<path d="m5 12 4 4L19 6"/>',
  refresh: '<path d="M20 10a8 8 0 1 0-2 8M20 4v6h-6"/>',
  chip: '<rect x="6" y="6" width="12" height="12" rx="3"/><path d="M9 2v4m6-4v4M9 18v4m6-4v4M2 9h4m-4 6h4m12-6h4m-4 6h4"/>',
  clock: '<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/>',
  device: '<rect x="3" y="4" width="18" height="13" rx="2"/><path d="M8 21h8m-4-4v4"/>',
  router: '<rect x="3" y="11" width="18" height="10" rx="3"/><path d="M6 11V5m12 6V5M7 16h.01m4 0h.01m5 0h2"/>'
 };
 function icon(name) {
  var span = document.createElement('span');
  span.className = 'orchard-icon';
  span.innerHTML = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' + (paths[name] || paths.services) + '</svg>';
  return span;
 }
 function setNavigation(open) {
  document.body.classList.toggle('orchard-nav-open', open);
  var sidebar = document.getElementById('orchard-sidebar');
  if (sidebar) { sidebar.inert = mobileQuery.matches && !open; sidebar.setAttribute('aria-hidden', String(mobileQuery.matches && !open)); }
  document.querySelectorAll('#maincontent,.orchard-footer').forEach(function (element) { element.inert = mobileQuery.matches && open; });
  var scrim = document.getElementById('orchard-scrim');
  if (scrim) scrim.hidden = !open;
  var toggle = document.getElementById('orchard-nav-toggle');
  if (toggle) toggle.setAttribute('aria-expanded', String(open));
 }
 function closeNavigation() {
  var sidebar = document.getElementById('orchard-sidebar');
  var restoreFocus = mobileQuery.matches && sidebar?.contains(document.activeElement);
  setNavigation(false);
  if (restoreFocus) document.getElementById('orchard-nav-toggle')?.focus();
 }
 function ready() {
  document.querySelectorAll('[data-icon]').forEach(function (element) { element.replaceChildren(icon(element.dataset.icon)); });
  document.querySelectorAll('[data-appearance]').forEach(function (button) {
   button.addEventListener('click', function () {
    applyAppearance(button.dataset.appearance);
    try { localStorage.setItem('orchard.appearance', appearance); } catch (_) {}
   });
  });
  applyAppearance(appearance);
  var toggle = document.getElementById('orchard-nav-toggle');
  var scrim = document.getElementById('orchard-scrim');
  if (toggle) toggle.addEventListener('click', function () {
   var open = !document.body.classList.contains('orchard-nav-open');
   setNavigation(open);
   if (open) document.querySelector('#orchard-sidebar a')?.focus();
  });
  if (scrim) scrim.addEventListener('click', closeNavigation);
  closeNavigation();
  if (mobileQuery.addEventListener) mobileQuery.addEventListener('change', closeNavigation);
  document.addEventListener('keydown', function (event) {
   if (event.key === 'Escape' && document.body.classList.contains('orchard-nav-open')) { closeNavigation(); if (toggle) toggle.focus(); }
   if (event.key === 'Tab' && mobileQuery.matches && document.body.classList.contains('orchard-nav-open')) {
    var controls = [toggle].concat(Array.from(document.querySelectorAll('#orchard-sidebar a[href],#orchard-sidebar button:not([disabled]),#orchard-sidebar summary'))).filter(function (element) { return element && element.getClientRects().length; });
    var index = controls.indexOf(document.activeElement);
    var next = event.shiftKey ? (index <= 0 ? controls.length - 1 : index - 1) : (index + 1) % controls.length;
    if (controls[next]) { event.preventDefault(); controls[next].focus(); }
   }
  });
  var login = document.querySelector('input[name="luci_password"]');
  if (login) {
   document.body.classList.add('orchard-login');
   login.autocomplete = 'current-password';
   var username = document.querySelector('input[name="luci_username"]');
   if (username) username.autocomplete = 'username';
  }
 }
 window.Orchard = { icon: icon, closeNavigation: closeNavigation, applyAppearance: applyAppearance };
 if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', ready);
 else ready();
})();
