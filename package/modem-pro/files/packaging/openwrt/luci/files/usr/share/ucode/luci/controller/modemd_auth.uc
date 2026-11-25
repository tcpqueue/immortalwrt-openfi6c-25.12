// LuCI 侧只读端点：把**当前已登录会话的 ID** 交给嵌在 LuCI 页面里的小窗。
//
// 为什么需要它（真机实测的约束）：
//   · LuCI 自己的会话 cookie 是 `sysauth_http`，带 **path=/cgi-bin/luci/**、SameSite=strict、
//     HttpOnly ⇒ 浏览器**不会**把它带到 modemd 的端口（:8899），JS 也读不到；
//   · 而 modemd 需要"你确实登录过路由器"这个事实才能放行（见 module/luciauth/luciauth.go）。
// 所以让 LuCI 这边（本端点，**只有已登录才能访问**）把会话 ID 交给页面，页面再拼参/写 cookie
// 传给 modemd。拿不到（没登录、或老 LuCI 没这个端点）时 modemd 会退回普通登录模式，功能不受影响。
//
// 端点：GET /cgi-bin/luci/modemd_auth/session  →  {"sid":"<32位会话ID>"} 或 {"sid":null}
// 安全性：GET 不带 CSRF token 也安全 —— 跨站请求带不上 SameSite=strict 的 cookie，
//         响应也没有 CORS 头（跨源读不到）；返回内容只有会话 ID，没有任何别的信息。
'use strict';

return {
	action_session: function() {
		http.prepare_content('application/json; charset=UTF-8');
		http.write_json({
			sid: (typeof ctx == 'object' && ctx && ctx.authsession) ? ctx.authsession : null
		});
	}
};
