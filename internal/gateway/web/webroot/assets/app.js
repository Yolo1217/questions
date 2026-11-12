// SHOP-OS 交易后台前端公共逻辑：登录态、请求封装、渲染辅助。
// 页面直接调用真实网关 /api/v1 接口，不使用任何本地假数据。
(function () {
  const API = '/api/v1';

  const ROLE_META = {
    buyer: { label: '买家', accounts: [['buyer1', '阿禾'], ['buyer2', '北舟'], ['buyer3', '橙子']] },
    warehouse: { label: '仓库', accounts: [['warehouse01', '仓管小周']] },
    cs: { label: '客服', accounts: [['support01', '客服小时']] },
    ops: { label: '运营', accounts: [['ops01', '运营阿运']] }
  };

  const money = (cents) => '¥' + (Number(cents || 0) / 100).toFixed(2);

  const STATUS_META = {
    CREATED: ['待支付', 'warn'],
    PAID: ['待发货', 'info'],
    REFUNDING: ['退款审核中', 'warn'],
    SHIPPED: ['已发货', 'ok'],
    CLOSED: ['已关闭', ''],
    REFUNDED: ['已退款', 'bad'],
    待处理: ['待处理', 'warn'],
    已闭环: ['已闭环', 'ok'],
    DONE: ['DONE', 'ok'],
    RECEIVED: ['RECEIVED', 'warn'],
    FAILED: ['FAILED', 'bad'],
    true: ['在售', 'ok'],
    false: ['下架', '']
  };

  function badge(value) {
    const key = value === true ? 'true' : value === false ? 'false' : String(value || '');
    const meta = STATUS_META[key] || [key || '-', ''];
    return `<span class="badge ${meta[1]}">${escapeHTML(meta[0])}</span>`;
  }

  function escapeHTML(text) {
    return String(text == null ? '' : text).replace(/[&<>"']/g, (ch) => (
      { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[ch]
    ));
  }

  function toast(message, kind) {
    const node = document.getElementById('toast');
    if (!node) return;
    node.textContent = message;
    node.className = 'toast show ' + (kind || '');
    clearTimeout(toast._timer);
    toast._timer = setTimeout(() => { node.className = 'toast ' + (kind || ''); }, 3600);
  }

  function tokenKey(role) { return 'shop.token.' + role; }
  function userKey(role) { return 'shop.user.' + role; }

  function session(role) {
    const token = localStorage.getItem(tokenKey(role)) || '';
    let user = {};
    try { user = JSON.parse(localStorage.getItem(userKey(role)) || '{}'); } catch (e) { user = {}; }
    return { token, user };
  }

  function clearSession(role) {
    localStorage.removeItem(tokenKey(role));
    localStorage.removeItem(userKey(role));
  }

  // 网关用 protoc-gen-go 的 json tag，字段为 snake_case；这里统一转 camelCase 再交给页面。
  function toCamel(value) {
    if (Array.isArray(value)) return value.map(toCamel);
    if (value && typeof value === 'object') {
      const out = {};
      Object.keys(value).forEach((key) => {
        const camel = key.replace(/_([a-z0-9])/g, (_, ch) => ch.toUpperCase());
        out[camel] = toCamel(value[key]);
      });
      return out;
    }
    return value;
  }

  // 统一响应：成功 {request_id, data}，失败 {error:{code,message}}
  async function api(path, options) {
    const opts = options || {};
    const headers = { 'Content-Type': 'application/json' };
    if (opts.token) headers.Authorization = 'Bearer ' + opts.token;
    const resp = await fetch(API + path, {
      method: opts.method || 'GET',
      headers,
      body: opts.body ? JSON.stringify(opts.body) : undefined
    });
    let payload = null;
    try { payload = await resp.json(); } catch (e) { payload = null; }
    const apiError = payload && payload.error;
    if (!resp.ok || apiError) {
      const code = (apiError && apiError.code) || 'http_' + resp.status;
      const message = (apiError && apiError.message) || ('HTTP ' + resp.status);
      const err = new Error(message);
      err.code = code;
      err.status = resp.status;
      throw err;
    }
    return toCamel((payload && payload.data !== undefined) ? payload.data : payload);
  }

  async function login(role, account, password) {
    const data = await api('/users/login', { method: 'POST', body: { account, password } });
    const user = data.user || {};
    // 兼容 snake_case 原始字段（api() 已转 camelCase，这里兜底 display_name）
    if (user.role && user.role !== role) {
      throw new Error('该账号角色为 ' + user.role + '，不能登录' + ROLE_META[role].label + '工作台');
    }
    localStorage.setItem(tokenKey(role), data.token);
    localStorage.setItem(userKey(role), JSON.stringify({ id: user.id, name: user.displayName, role: user.role }));
    return data;
  }

  // mount 渲染登录区/工作区切换与顶部会话信息，登录成功后回调 onReady。
  function mount(role, onReady) {
    const meta = ROLE_META[role];
    const loginPanel = document.getElementById('loginPanel');
    const main = document.getElementById('main');
    const sessionBox = document.getElementById('session');

    function renderLogin() {
      loginPanel.innerHTML = `
        <div class="card">
          <h1>${meta.label}工作台登录</h1>
          <p class="hint">预置账号，初始密码 123456</p>
          <div class="field">
            <label for="account">账号</label>
            <select id="account">${meta.accounts.map(([acc, name]) => `<option value="${acc}">${acc}（${name}）</option>`).join('')}</select>
          </div>
          <div class="field">
            <label for="password">密码</label>
            <input id="password" type="password" value="123456" autocomplete="current-password" />
          </div>
          <button class="btn primary" id="loginBtn" style="width:100%">登录</button>
          <p class="hint" style="margin-top:12px">当前身份由 JWT 决定，页面无法伪造他人身份。</p>
        </div>`;
      loginPanel.hidden = false;
      main.hidden = true;
      sessionBox.innerHTML = '';
      const submit = async () => {
        const btn = document.getElementById('loginBtn');
        btn.disabled = true;
        try {
          await login(role, document.getElementById('account').value, document.getElementById('password').value);
          toast('登录成功', 'ok');
          start();
        } catch (err) {
          toast('登录失败：' + err.message, 'bad');
        } finally {
          btn.disabled = false;
        }
      };
      document.getElementById('loginBtn').onclick = submit;
      document.getElementById('password').onkeydown = (e) => { if (e.key === 'Enter') submit(); };
    }

    function start() {
      const s = session(role);
      loginPanel.hidden = true;
      loginPanel.innerHTML = '';
      main.hidden = false;
      sessionBox.innerHTML = `
        <span class="badge info">${meta.label}</span>
        <span>${escapeHTML(s.user.name || s.user.id || '')}</span>
        <button class="btn sm ghost" id="logoutBtn">退出</button>`;
      document.getElementById('logoutBtn').onclick = () => {
        clearSession(role);
        renderLogin();
      };
      onReady(s);
    }

    if (session(role).token) start(); else renderLogin();
  }

  function openDrawer(title, html) {
    const drawer = document.getElementById('drawer');
    document.getElementById('drawerTitle').textContent = title;
    document.getElementById('drawerBody').innerHTML = html;
    drawer.classList.add('open');
  }

  function closeDrawer() {
    document.getElementById('drawer').classList.remove('open');
  }

  window.Shop = { api, mount, toast, money, badge, escapeHTML, session, openDrawer, closeDrawer, STATUS_META };
})();
