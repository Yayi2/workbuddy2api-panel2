'use strict';
/* ── 状态 ─────────────────────────────────────────────────────────── */
const LS_KEY = 'wb2api.key', LS_THEME = 'wb2api.theme';
let theme = localStorage.getItem(LS_THEME) || 'auto';   // auto | light | dark
let view = 'accounts';
let overviewData = null, cfgLoaded = null;
let logPin = true, loginState = null, loginTimer = null;
let refTimer = null;
// 站点筛选：all | cn | intl（只影响渲染，不影响请求与轮询）。
let regionFilter = 'all';
// 站点能力表（后端 /panel/api/regions 拉取；拉取失败时用内置兜底，面板不至于不可用）。
let regionMeta = {
  cn:   { label: '国内站', growth: true,  short: '国内站' },
  intl: { label: '国际站', growth: false, short: '国际站' },
};

const $ = id => document.getElementById(id);

/* ── 主题 ─────────────────────────────────────────────────────────── */
/* 两态翻转（浅/深），首次访问跟随系统偏好；点击总是切换可见外观，符合直觉。 */
function effTheme() {
  return theme === 'auto' ? (matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark') : theme;
}
function applyTheme() {
  const eff = effTheme();
  document.documentElement.dataset.theme = eff;
  $('icoTheme').innerHTML = eff === 'light'
    ? '<circle cx="8" cy="8" r="3"/><path d="M8 1v2M8 13v2M1 8h2M13 8h2M3.2 3.2l1.4 1.4M11.4 11.4l1.4 1.4M12.8 3.2l-1.4 1.4M4.6 11.4l-1.4 1.4"/>'
    : '<path d="M13.2 9.6A5.6 5.6 0 0 1 6.4 2.8a5.6 5.6 0 1 0 6.8 6.8z"/>';
  $('btnTheme').title = eff === 'light' ? '切换到深色' : '切换到浅色';
}
addEventListener('change', applyTheme);
$('btnTheme').onclick = () => {
  theme = effTheme() === 'light' ? 'dark' : 'light';
  localStorage.setItem(LS_THEME, theme);
  applyTheme();
};
applyTheme();

/* ── 请求 ─────────────────────────────────────────────────────────── */
async function api(path, opts = {}) {
  const h = Object.assign({}, opts.headers || {});
  const k = localStorage.getItem(LS_KEY);
  if (k) h['Authorization'] = 'Bearer ' + k;
  if (opts.body) h['Content-Type'] = 'application/json';
  const r = await fetch('/panel/api/' + path, Object.assign({}, opts, { headers: h }));
  if (r.status === 401) { openKey(); throw new Error('密钥无效或未填写'); }
  const d = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(d.error || ('HTTP ' + r.status));
  return d;
}
function toast(msg, cls) {
  const el = document.createElement('div');
  el.className = 'tst ' + (cls || '');
  el.textContent = msg;
  $('toasts').appendChild(el);
  setTimeout(() => el.remove(), 3600);
}
// esc 文本/属性双安全转义。不能只用 div.innerHTML（它转义 <>& 但不转义引号），
// 否则字符串拼进 HTML 属性（如 title="uid: ..."）时引号可闭合属性并注入事件处理器。
// 显式替换 5 个字符：& < > " '（& 必须最先，避免二次转义）。
function esc(s) {
  return String(s == null ? '' : s)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}
function ago(iso) {
  if (!iso || iso.startsWith('0001-')) return '—';
  const s = (Date.now() - new Date(iso)) / 1000;
  if (s < 0) return '刚刚';
  if (s < 60) return Math.floor(s) + ' 秒前';
  if (s < 3600) return Math.floor(s / 60) + ' 分钟前';
  if (s < 86400) return Math.floor(s / 3600) + ' 小时前';
  return Math.floor(s / 86400) + ' 天前';
}
function dur(sec) {
  sec = Math.max(0, Math.round(sec));
  const h = Math.floor(sec / 3600), m = Math.floor(sec % 3600 / 60), s = sec % 60;
  return h ? h + '时' + String(m).padStart(2, '0') + '分' : m ? m + '分' + String(s).padStart(2, '0') + '秒' : s + '秒';
}
// fmtTime 把 ISO/时间戳格式化为本地墙钟 "MM-DD HH:MM:SS"，供"恢复于 …"展示。
// 冷却截止是绝对时刻，用本地时区渲染才能和用户看到的钟一致（后端 UTC+8 解析）。
function fmtTime(t) {
  const d = t instanceof Date ? t : new Date(t);
  if (isNaN(d.getTime()) || d.getFullYear() < 2000) return '';
  const p = n => String(n).padStart(2, '0');
  return p(d.getMonth() + 1) + '-' + p(d.getDate()) + ' ' + p(d.getHours()) + ':' + p(d.getMinutes()) + ':' + p(d.getSeconds());
}

/* ── 密钥门 ───────────────────────────────────────────────────────── */
function openKey() { $('keyVeil').classList.add('on'); setTimeout(() => $('keyInput').focus(), 60); }
$('btnKey').onclick = async () => {
  const v = $('keyInput').value.trim();
  if (!v) return;
  localStorage.setItem(LS_KEY, v);
  try {
    await api('overview');
    $('keyErr').hidden = true;
    $('keyVeil').classList.remove('on');
    start();
  } catch (e) { $('keyErr').hidden = false; }
};
$('keyInput').addEventListener('keydown', e => { if (e.key === 'Enter') $('btnKey').click(); });

/* ── 路由 ─────────────────────────────────────────────────────────── */
const TITLES = { accounts: '账号池', models: '模型与档位', config: '配置', logs: '运行日志' };
function go(v) {
  view = v;
  document.querySelectorAll('.view').forEach(s => s.hidden = s.id !== 'view-' + v);
  document.querySelectorAll('.nav a').forEach(a => a.classList.toggle('on', a.dataset.view === v));
  $('ttl').textContent = TITLES[v];
  if (v === 'models' && !$('mdBody').children.length) loadModels();
  if (v === 'config') loadConfig();
  if (v === 'logs') loadLogs();
}
document.querySelectorAll('.nav a').forEach(a => a.onclick = e => { e.preventDefault(); go(a.dataset.view); history.replaceState(null, '', '#' + a.dataset.view); });
go((location.hash || '#accounts').slice(1) in TITLES ? (location.hash || '#accounts').slice(1) : 'accounts');

/* ── 账号池 ───────────────────────────────────────────────────────── */
// regionOf 返回账号站点标识；老账号（后端未透出）回退国内站，与后端口径一致。
function regionOf(s) { return s.region === 'intl' ? 'intl' : 'cn'; }
function regionTag(region) {
  const r = region === 'intl' ? 'intl' : 'cn';
  const label = (regionMeta[r] && regionMeta[r].label) || (r === 'intl' ? '国际站' : '国内站');
  return '<span class="tag ' + r + '">' + esc(label) + '</span>';
}
// growthOf 报告账号所属站点是否支持成长中心（积分任务/签到/旅行）。
function growthOf(s) { return s.region_growth !== undefined ? !!s.region_growth : regionOf(s) !== 'intl'; }

function renderAccounts(list) {
  const tb = $('accBody');
  if (!list.length) {
    tb.innerHTML = '<tr><td colspan="9"><div class="empty"><div class="big">账号池是空的</div>点击右上角「添加账号」，用浏览器登录一个 WorkBuddy 账号</div></td></tr>';
    return;
  }
  // 站点筛选：只影响展示，不影响任何请求。
  const shown = regionFilter === 'all' ? list : list.filter(s => regionOf(s) === regionFilter);
  if (!shown.length) {
    const lb = (regionMeta[regionFilter] && regionMeta[regionFilter].label) || regionFilter;
    tb.innerHTML = '<tr><td colspan="9"><div class="empty">当前筛选下（' + esc(lb) + '）没有账号</div></td></tr>';
    return;
  }
  const maxCred = Math.max(1, ...shown.map(s => s.credits || 0));
  tb.innerHTML = shown.map(s => {
    const bl = (new Date(s.breaker_until || 0) - Date.now()) / 1000;
    const cool = Math.max(s.cool_remaining_sec || 0, bl > 0 ? bl : 0);
    const growth = growthOf(s);
    let cls = '', tag;
    if (s.disabled) { cls = 'off'; tag = '<span class="tag bad">已禁用</span>'; }
    else if (cool > 0) {
      cls = 'cool';
      const kind = bl > (s.cool_remaining_sec || 0) ? '熔断' : (s.cool_kind === 'hard_credit' ? '积分冷却' : '限流冷却');
      tag = '<span class="tag warn">' + kind + ' · ' + dur(cool) + '</span>';
      // 模型级 6004 限流：这是"能换模型继续用"的可恢复状态，必须和账号级限流区分开，
      // 否则用户只看到"限流冷却"会误以为整个号废了（实际切模型立即可用）。
      if (s.model_rate_limit) {
        tag += '<span class="tag ok" style="margin-left:4px">换模型可用</span>';
      }
    } else tag = '<span class="tag ok">可用</span>' + (s.in_flight ? '' : '');
    // 恢复时间 + 触发模型：面板要能直接读出"什么时候恢复、被哪个模型限的"。
    let noteTxt = s.reason || '';
    if (cool > 0) {
      const until = s.cool_remaining_sec >= bl && s.until
        ? new Date(s.until) : new Date(s.breaker_until || 0);
      if (until && !isNaN(until)) {
        noteTxt = '恢复于 ' + fmtTime(until) + (noteTxt ? ' · ' + noteTxt : '');
      }
      if (s.model_rate_model) noteTxt += ' · 受限模型 ' + s.model_rate_model;
    }
    const note = noteTxt ? '<div class="hint" style="font-size:11.5px;color:var(--ink-3);margin-top:3px">' + esc(noteTxt) + '</div>' : '';
    const short = s.uid.length > 16 ? s.uid.slice(0, 16) + '…' : s.uid;
    const cred = s.credits == null ? '—' : s.credits;
    const frozen = s.disabled || cool > 0;
    // 成长中心按钮：国际站无该能力 → 置灰并给出原因，避免点击后撞 400。
    const growthNA = ' title="' + esc((regionMeta[regionOf(s)] && regionMeta[regionOf(s)].label) || '该站点') + '不提供成长中心（积分任务/签到/旅行仅国内站提供）" disabled';
    return '<tr class="' + cls + '" title="uid: ' + esc(s.uid) + '">' +
      '<td class="mark" aria-hidden="true"><i></i></td>' +
      '<td class="who"><div class="nm">' + (s.nickname ? esc(s.nickname) : '<span style="color:var(--ink-3)">未命名</span>') + '</div><div class="id">' + esc(short) + '</div></td>' +
      '<td>' + regionTag(regionOf(s)) + '</td>' +
      '<td>' + tag + note + '</td>' +
      '<td class="cred"><div class="n">' + cred + '</div><div class="bar"><i style="width:' + Math.round((s.credits || 0) / maxCred * 100) + '%"></i></div></td>' +
      '<td class="num">' + (s.success_count || 0) + ' <span style="color:var(--ink-3)">/</span> <span style="color:var(--bad)">' + (s.err_total || 0) + '</span></td>' +
      '<td class="num">' + (s.in_flight || 0) + '</td>' +
      '<td class="num" style="color:var(--ink-3)">' + ago(s.last_success) + '</td>' +
      '<td class="acts">' +
        '<button class="xs ghost" data-a="checkin" data-u="' + esc(s.uid) + '"' + (growth ? '' : growthNA) + '>签到</button>' +
        '<button class="xs ghost" data-a="balance" data-u="' + esc(s.uid) + '">余额</button>' +
        '<button class="xs ghost" data-a="tasks" data-u="' + esc(s.uid) + '"' + (growth ? '' : growthNA) + '>任务</button>' +
        (frozen ? '<button class="xs primary" data-a="revive" data-u="' + esc(s.uid) + '">解冻</button>'
                : '<button class="xs ghost" data-a="disable" data-u="' + esc(s.uid) + '">禁用</button>') +
        '<button class="xs ghost danger" data-a="remove" data-u="' + esc(s.uid) + '">移除</button>' +
      '</td></tr>';
  }).join('');
}

// renderRegionFilter 渲染站点筛选条：只展示池中实际存在的站点，
// 当前筛选站点消失（账号被移除）时自动回落到「全部」，避免停在空列表上。
function renderRegionFilter(data) {
  const counts = data.regions || [];
  const known = new Set(data.known_regions || []);
  const btns = document.querySelectorAll('#regionFilter button.rf');
  btns.forEach(b => {
    const r = b.dataset.r;
    const hit = r === 'all' ? true : known.has(r);
    b.hidden = !hit;
    b.classList.toggle('on', r === regionFilter);
    if (r !== 'all') {
      const c = counts.find(x => x.region === r);
      const label = (c && c.label) || (regionMeta[r] && regionMeta[r].label) || r;
      b.textContent = label + (c ? ' ' + c.total : '');
    }
  });
  if (regionFilter !== 'all' && !known.has(regionFilter)) regionFilter = 'all';
  // 汇总：混挂时提示两站分布，便于一眼看出池的成分。
  const parts = counts.filter(c => c.total > 0).map(c => c.label + ' ' + c.healthy + '/' + c.total);
  $('rfNote').textContent = parts.length ? parts.join(' · ') + '（可用/总数）' : '';
}

// 站点筛选按钮事件。
document.querySelectorAll('#regionFilter button.rf').forEach(b => {
  b.onclick = () => {
    regionFilter = b.dataset.r;
    renderRegionFilter(overviewData || {});
    renderAccounts((overviewData && overviewData.accounts) || []);
  };
});

async function loadOverview(quiet) {
  try {
    const d = await api('overview');
    overviewData = d;
    $('sTotal').textContent = d.total;
    $('sHealthy').textContent = d.healthy;
    $('sCooling').textContent = d.cooling;
    $('sDisabled').textContent = d.disabled;
    $('sCredits').textContent = (d.accounts || []).reduce((a, s) => a + (s.credits || 0), 0);
    $('sSticky').textContent = d.sticky_sessions;
    $('navSub').textContent = 'v' + d.version;
    $('navVer').textContent = 'v' + d.version;
    $('navRedis').textContent = d.redis_mode === 'upstash' ? 'Redis 镜像' : '本地内存';
    $('navState').textContent = d.healthy > 0 ? '服务正常' : (d.total ? '无可用账号' : '待添加账号');
    const p = $('navPulse');
    p.className = 'pulse' + (d.healthy > 0 ? '' : (d.total ? ' warn' : ' bad'));
    $('accNote').textContent = d.in_flight_full ? d.in_flight_full + ' 个账号在途占满' : '';
    const up = Math.floor(d.uptime_sec);
    $('subMeta').textContent = '运行 ' + (up >= 86400 ? Math.floor(up / 86400) + ' 天 ' : '') + Math.floor(up % 86400 / 3600) + ' 时 ' + Math.floor(up % 3600 / 60) + ' 分';
    renderRegionFilter(d);
    renderAccounts(d.accounts || []);
  } catch (e) { if (!quiet) toast(e.message, 'err'); }
}

// loadRegions 拉取站点能力表（标签 / 是否有成长中心 / 登录方式）。
// 失败时保留内置兜底表——面板的站点列与置灰逻辑不能因为一个只读接口失败而失效。
async function loadRegions() {
  try {
    const d = await api('regions');
    (d.regions || []).forEach(r => {
      regionMeta[r.region] = {
        label: r.label, growth: !!r.growth,
        short: r.label, chat_base: r.chat_base, origin: r.origin,
        platform: r.platform, login_hint: r.login_hint,
        login_ttl_sec: r.login_ttl_sec,
      };
    });
  } catch (e) { /* 保留内置兜底 */ }
}

$('accBody').addEventListener('click', async ev => {
  const b = ev.target.closest('button[data-a]');
  if (!b) return;
  const u = b.dataset.u, a = b.dataset.a;
  if (a === 'remove' && !confirm('移除账号将删除池状态与 auths/ 下的凭证文件，且不可恢复。确认移除？')) return;
  if (a === 'disable' && !confirm('禁用后该账号不再参与选号，需手动解冻才能恢复。确认禁用？')) return;
  b.disabled = true;
  try {
    if (a === 'checkin') {
      const r = await api('accounts/' + encodeURIComponent(u) + '/checkin', { method: 'POST' });
      toast('签到完成' + (r.credits != null ? '，积分 ' + r.credits : '') + (r.checkin_message ? '（' + r.checkin_message + '）' : ''), 'ok');
    } else if (a === 'balance') {
      const r = await api('accounts/' + encodeURIComponent(u) + '/balance', { method: 'POST' });
      toast('余额已更新：' + r.credits, 'ok');
    } else if (a === 'revive') {
      await api('accounts/' + encodeURIComponent(u) + '/revive', { method: 'POST' });
      toast('已解冻', 'ok');
    } else if (a === 'disable') {
      await api('accounts/' + encodeURIComponent(u) + '/disable', { method: 'POST' });
      toast('已禁用', 'ok');
    } else if (a === 'tasks') {
      openTasks(u);
    } else if (a === 'remove') {
      const r = await api('accounts/' + encodeURIComponent(u) + '/remove', { method: 'POST' });
      toast(r.file_error ? '已移除（凭证文件删除失败：' + r.file_error + '）' : '已移除', 'ok');
    }
  } catch (e) { toast(e.message, 'err'); }
  finally { b.disabled = false; loadOverview(true); }
});

$('btnCheckinAll').onclick = async () => {
  try { await api('checkin_all', { method: 'POST' }); toast('全部签到已开始，结果见日志', 'ok'); }
  catch (e) { toast(e.message, 'err'); }
};
$('btnKeepaliveAll').onclick = async () => {
  try { await api('keepalive_all', { method: 'POST' }); toast('全部保活已开始，结果见日志', 'ok'); }
  catch (e) { toast(e.message, 'err'); }
};
$('btnTravelAll').onclick = async () => {
  try { await api('travel_all', { method: 'POST' }); toast('旅行巡检已开始（含领养链路），结果见日志', 'ok'); }
  catch (e) { toast(e.message, 'err'); }
};
$('btnActivityAll').onclick = async () => {
  try { await api('activity_all', { method: 'POST' }); toast('活跃上报已开始，结果见日志', 'ok'); }
  catch (e) { toast(e.message, 'err'); }
};

/* ── 模型 ─────────────────────────────────────────────────────────── */
// 当前展示的模型站点（"cn" 实时查询 / "intl" 静态清单）。
let mdRegion = 'cn';

// copyText 复制到剪贴板 + 给按钮一次性成功反馈。
// 失败时降级为「选中文本提示手动复制」——file:// 或非安全上下文下
// navigator.clipboard 可能不可用，不能静默失败。
async function copyText(text, btn) {
  const flash = ok => {
    if (!btn) return;
    const old = btn.textContent;
    btn.classList.add('copied');
    btn.textContent = ok ? '已复制' : '复制失败';
    setTimeout(() => { btn.classList.remove('copied'); btn.textContent = old; }, 1200);
  };
  try {
    if (navigator.clipboard && isSecureContext) {
      await navigator.clipboard.writeText(text);
    } else {
      // 非安全上下文（如用 IP + HTTP 访问）回退到 execCommand。
      const ta = document.createElement('textarea');
      ta.value = text;
      ta.style.position = 'fixed';
      ta.style.opacity = '0';
      document.body.appendChild(ta);
      ta.select();
      const ok = document.execCommand('copy');
      ta.remove();
      if (!ok) throw new Error('execCommand copy failed');
    }
    flash(true);
  } catch (e) {
    flash(false);
    toast('复制失败，请手动选择复制', 'err');
  }
}

// apiBaseURL 由浏览器当前地址推导网关入口。
// 用 location 而非后端上报的 listen：经过反代/改端口/域名访问时，
// location 才是客户端真正能连上的地址（listen 常是 :7863 但对外是别的端口）。
function apiOrigin() { return location.origin; }

// refreshAPIURLs 把 API 地址写进模型页的地址条（含实际协议/主机/端口）。
function refreshAPIURLs() {
  const o = apiOrigin();
  const set = (id, txt) => { const el = $(id); if (el) el.textContent = txt; };
  set('apiBase', o + '/v1');
  set('apiChat', o + '/v1/chat/completions');
  set('apiModels', o + '/v1/models');
}

// 地址条复制按钮（事件委托，含动态写入的地址）。
document.addEventListener('click', ev => {
  const b = ev.target.closest('button[data-copy]');
  if (!b) return;
  const id = b.dataset.copy;
  const el = $(id);
  if (!el) return;
  copyText(el.textContent.trim(), b);
});

// loadModels 按 mdRegion 拉取模型清单。
//
// 快速连点站点切换会产生并发请求，后发的可能先回。用 reqSeq 单调序号丢弃
// 过期响应，避免"国际站的清单显示在国内站标签下"。
let mdReqSeq = 0;
async function loadModels() {
  const tb = $('mdBody');
  const isStatic = mdRegion === 'intl';
  const seq = ++mdReqSeq;
  const region = mdRegion;
  tb.innerHTML = '<tr><td colspan="8"><div class="empty">' +
    (isStatic ? '加载静态清单…' : '正在向上游查询…') + '</div></td></tr>';
  try {
    const d = await api('models?region=' + encodeURIComponent(region));
    if (seq !== mdReqSeq) return; // 已被更新的请求取代
    const list = d.models || [];
    if (!list.length) { tb.innerHTML = '<tr><td colspan="8"><div class="empty">上游未返回模型</div></td></tr>'; return; }
    tb.innerHTML = list.map(m => {
      const eff = (m.supported_efforts || []).slice();
      if (m.can_disable_thinking && eff.length && !eff.includes('off')) eff.push('off（可关）');
      // 静态清单没有思考档位元数据：显示为「—」而不是误报"不支持思考"。
      const effs = eff.length ? eff.map(e => '<span class="tag warn">' + esc(e) + '</span>').join(' ')
        : '<span style="color:var(--ink-3);font-size:12.5px">' +
          (isStatic ? '—（静态清单无档位信息）'
                    : (m.supports_reasoning ? '固定档 · 默认 ' + esc(m.default_effort || '?') : '不支持思考')) + '</span>';
      return '<tr><td class="mark" aria-hidden="true"><i></i></td>' +
        '<td class="who"><div class="nm">' + esc(m.id) + '</div><div class="id">' + esc(m.name || '') + '</div></td>' +
        '<td class="num">' + (m.credits ? esc(m.credits) : '—') + '</td>' +
        '<td>' + (m.default_effort ? '<span class="tag ok">' + esc(m.default_effort) + '</span>' : '<span style="color:var(--ink-3)">—</span>') + '</td>' +
        '<td class="efs" style="white-space:normal">' + effs + '</td>' +
        '<td class="num">' + (m.context_length ? Math.round(m.context_length / 1000) + 'K' : '—') + '</td>' +
        '<td class="num">' + (m.max_output_tokens ? Math.round(m.max_output_tokens / 1000) + 'K' : '—') + '</td>' +
        '<td class="cp"><button class="xs ghost" data-cp="' + esc(m.id) + '" title="复制模型名">复制</button></td></tr>';
    }).join('');
    const srcNote = d.source === 'static' ? '静态清单' : '实时查询';
    $('mdNote').textContent = list.length + ' 个模型 · ' + srcNote + (d.source === 'static' ? '' : ' · 已刷新降级缓存');
    $('mdNote').title = d.message || '';
    $('mdSrcNote').textContent = (regionMeta[mdRegion] && regionMeta[mdRegion].label || mdRegion) +
      ' · ' + srcNote + (d.source === 'static' ? '（该站无模型清单接口）' : '');
    // 静态清单没有档位/倍率数据，隐藏这两列避免整列「—」的噪声。
    setColVisible('.acc.md', isStatic);
  } catch (e) {
    if (seq !== mdReqSeq) return; // 过期请求的错误不覆盖新站点的展示
    $('mdNote').textContent = '查询失败';
    $('mdNote').title = e.message;
    tb.innerHTML = '<tr><td colspan="8"><div class="empty">' + esc(e.message) +
      '<div style="margin-top:6px;color:var(--ink-3);font-size:12.5px">' +
      '国内站清单需池中有可用的国内站账号（国际站账号不提供该接口）。' +
      '切换到「国际站」可查看国际站实测可用模型。</div>' +
      '</div></td></tr>';
  }
}

// setColVisible 静态清单模式下隐藏「积分倍率」「默认档」「最大输出」列。
function setColVisible(sel, hide) {
  const t = document.querySelector(sel);
  if (!t) return;
  // 列序：mark, 模型, 积分倍率, 默认档, 思考档位, 上下文, 最大输出, 复制
  [3, 4, 7].forEach(n => {
    const th = t.querySelector('thead tr th:nth-child(' + n + ')');
    if (th) th.hidden = hide;
  });
  t.querySelectorAll('tbody tr').forEach(tr => {
    [3, 4, 7].forEach(n => {
      const td = tr.querySelector('td:nth-child(' + n + ')');
      if (td) td.hidden = hide;
    });
  });
}

// 模型行内「复制」按钮（事件委托）。
$('mdBody').addEventListener('click', ev => {
  const b = ev.target.closest('button[data-cp]');
  if (!b) return;
  copyText(b.dataset.cp, b);
});

// 复制全部模型名（每行一个，便于粘进客户端配置）。
$('btnCopyAllModels').onclick = () => {
  const names = Array.from($('mdBody').querySelectorAll('button[data-cp]')).map(b => b.dataset.cp);
  if (!names.length) { toast('当前没有可复制的模型', 'err'); return; }
  copyText(names.join('\n'), $('btnCopyAllModels'));
};

// 站点切换。
document.querySelectorAll('#mdFilter button[data-mr]').forEach(b => {
  b.classList.toggle('on', b.dataset.mr === mdRegion);
  b.onclick = () => {
    mdRegion = b.dataset.mr;
    document.querySelectorAll('#mdFilter button[data-mr]').forEach(x => x.classList.toggle('on', x === b));
    loadModels();
  };
});

$('btnModels').onclick = loadModels;

/* ── 使用顺序 ─────────────────────────────────────────────────────── */
// 当前的策略与顺序（弹窗内缓存，避免每次操作都重拉）。
let orderData = null;

function openOrder() {
  $('orderVeil').classList.add('on');
  loadOrder();
}
function closeOrder() { $('orderVeil').classList.remove('on'); }
$('btnCloseOrder').onclick = closeOrder;
$('btnOrderReload').onclick = loadOrder;
$('btnOrder').onclick = openOrder;

// loadOrder 拉取策略 + 顺序并渲染。
async function loadOrder() {
  const st = $('orderState'), ol = $('orderList');
  st.hidden = false;
  st.innerHTML = '<span class="dots">加载中</span>';
  ol.hidden = true;
  try {
    const d = await api('pool/order');
    orderData = d;
    renderOrderStrategy(d);
    renderOrderList(d);
  } catch (e) {
    st.className = 'state err';
    st.textContent = e.message;
  }
}

// renderOrderStrategy 渲染策略单选项与说明。
function renderOrderStrategy(d) {
  const cur = d.strategy || 'weighted';
  $('orderStrategy').innerHTML = (d.strategies || []).map(s =>
    '<label title="' + esc(s.desc) + '">' +
      '<span class="sn"><input type="radio" name="orderStrat" value="' + esc(s.id) + '"' +
        (s.id === cur ? ' checked' : '') + '>' + esc(s.label) +
        (s.id === cur ? ' <span class="tag ok">当前</span>' : '') + '</span>' +
      '<span class="sd">' + esc(s.desc) + '</span>' +
    '</label>').join('');

  const note = $('orderStratNote');
  if (cur === 'weighted') {
    note.textContent = '当前为加权随机：顺序不参与选号（这是默认策略，会刻意打散热点）。' +
      '想让顺序生效，请选「手动优先级」或「轮流使用」。';
    note.style.color = 'var(--warn)';
  } else if (cur === 'priority') {
    note.textContent = '手动优先级：只要排在最前的账号可用就一直用它，它冷却/禁用后才自动用下一个。';
    note.style.color = 'var(--ink-3)';
  } else {
    note.textContent = '轮流使用：每次请求按顺序换下一个账号，均摊各号额度。';
    note.style.color = 'var(--ink-3)';
  }
}

// renderOrderList 渲染可排序的账号列表。
function renderOrderList(d) {
  const st = $('orderState'), ol = $('orderList');
  const list = d.order || [];
  if (!list.length) {
    st.className = 'state';
    st.textContent = '账号池是空的，先添加账号再排序。';
    ol.hidden = true;
    return;
  }
  st.hidden = true;
  ol.hidden = false;
  // 策略为 priority 时标出"当前实际会用的号"（第一个可用者），把抽象顺序落到实际行为上。
  let leadUID = null;
  if (d.strategy === 'priority') {
    const first = list.find(x => x.healthy);
    leadUID = first ? first.uid : null;
  }
  ol.innerHTML = list.map((x, i) => {
    // 排序视图的徽章要和账号表口径一致：冷却要带剩余时长与模型级标记，
    // 只写"冷却中"会让用户无法判断这个号还要等多久、能不能换模型绕开。
    let badge;
    if (x.disabled) badge = '<span class="tag bad">已禁用</span>';
    else if (x.cooling) {
      const sec = Math.max(x.cool_remaining_sec || 0, (new Date(x.breaker_until || 0) - Date.now()) / 1000);
      const k = x.breaker_until && new Date(x.breaker_until) > Date.now() && sec > (x.cool_remaining_sec || 0)
        ? '熔断' : (x.cool_kind === 'hard_credit' ? '积分冷却' : '限流冷却');
      badge = '<span class="tag warn">' + k + ' · ' + dur(sec) + '</span>';
      if (x.model_rate_limit) badge += '<span class="tag ok" style="margin-left:4px">换模型可用</span>';
    } else badge = '<span class="tag ok">可用</span>';
    // draggable 挂在 li 上（整行可拖），但鼠标需落在 .grip 上才启动——
    // 否则在行内选文本、点按钮都会误触拖拽。见 dragstart 里的判断。
    return '<li class="' + (x.uid === leadUID ? 'lead' : '') + '" draggable="true"' +
      ' data-uid="' + esc(x.uid) + '" data-idx="' + i + '" title="uid: ' + esc(x.uid) + '">' +
      '<span class="grip" title="拖动排序">⠿</span>' +
      '<span class="rank">' + (i + 1) + '</span>' +
      '<span class="oname"><span class="onm">' + (x.nickname ? esc(x.nickname) : '<span style="color:var(--ink-3)">未命名</span>') +
        ' <span class="tag ' + (x.region === 'intl' ? 'intl' : 'cn') + '">' + esc(x.label || '') + '</span></span>' +
        '<span class="oid">' + esc(x.uid) + '</span></span>' +
      badge +
      '<span class="oacts">' +
        '<button class="xs ghost" data-omv="up" data-ou="' + esc(x.uid) + '"' + (i === 0 ? ' disabled' : '') + ' title="上移">↑</button>' +
        '<button class="xs ghost" data-omv="down" data-ou="' + esc(x.uid) + '"' + (i === list.length - 1 ? ' disabled' : '') + ' title="下移">↓</button>' +
        '<button class="xs ghost" data-omv="top" data-ou="' + esc(x.uid) + '"' + (i === 0 ? ' disabled' : '') + ' title="移到最前（最高优先）">置顶</button>' +
      '</span></li>';
  }).join('');
  if (leadUID) {
    ol.querySelectorAll('li').forEach(li => {
      const u = li.querySelector('[data-ou]');
      if (u && u.dataset.ou === leadUID) li.classList.add('lead');
    });
  }
}

// 排序按钮（事件委托）。
$('orderList').addEventListener('click', async ev => {
  const b = ev.target.closest('button[data-omv]');
  if (!b) return;
  const uid = b.dataset.ou, mv = b.dataset.omv;
  b.disabled = true;
  try {
    let body;
    if (mv === 'top') body = { uid, to: 0 };
    else body = { uid, delta: mv === 'up' ? -1 : 1 };
    const d = await api('pool/order', { method: 'POST', body: JSON.stringify(body) });
    orderData = Object.assign({}, orderData, d);
    // 用返回的顺序重新渲染（含策略信息），保持列表与服务端一致。
    await loadOrder();
    if (d.message) toast(d.message, 'ok');
  } catch (e) {
    toast(e.message, 'err');
    await loadOrder();
  }
});

// ── 拖拽排序（HTML5 DnD）─────────────────────────────────────────────
//
// 交互约定：
//   - 只有按住左侧拖拽柄（.grip）才能拖动。否则行内选文本、点 ↑↓ 按钮都会
//     误触发 dragstart（HTML5 的 draggable 判定基于元素而非鼠标起点）。
//   - 拖动时用一条插入线（.drop-before/.drop-after）指示落点，松手才提交。
//   - 落点用「目标行的 uid + 前/后」表达，换算成服务端的 to 索引，
//     避免把 DOM 索引直接当成服务端索引（两者在边界情况下会差 1）。
let dragUID = null;

$('orderList').addEventListener('dragstart', ev => {
  const li = ev.target.closest('li[data-uid]');
  if (!li) return;
  // 必须从拖拽柄启动（见上方交互约定）。
  if (!ev.target.closest('.grip')) { ev.preventDefault(); return; }
  dragUID = li.dataset.uid;
  li.classList.add('dragging');
  // Firefox 要求设置数据才会真正开始拖拽。
  try { ev.dataTransfer.setData('text/plain', dragUID); } catch (e) { /* 忽略 */ }
  ev.dataTransfer.effectAllowed = 'move';
});

$('orderList').addEventListener('dragend', () => {
  dragUID = null;
  clearDropMarks();
  const d = $('orderList').querySelector('li.dragging');
  if (d) d.classList.remove('dragging');
});

$('orderList').addEventListener('dragover', ev => {
  if (!dragUID) return;
  ev.preventDefault(); // 允许放置
  ev.dataTransfer.dropEffect = 'move';
  const li = ev.target.closest('li[data-uid]');
  clearDropMarks();
  if (!li || li.dataset.uid === dragUID) return;
  // 以该行中线判断插到上半还是下半。
  const r = li.getBoundingClientRect();
  const after = (ev.clientY - r.top) > r.height / 2;
  li.classList.add(after ? 'drop-after' : 'drop-before');
});

$('orderList').addEventListener('dragleave', ev => {
  // 离开列表区域时清掉指示线（进入子元素也会触发 dragleave，故判断 relatedTarget）。
  if (!ev.relatedTarget || !$('orderList').contains(ev.relatedTarget)) clearDropMarks();
});

$('orderList').addEventListener('drop', async ev => {
  if (!dragUID) return;
  ev.preventDefault();
  const li = ev.target.closest('li[data-uid]');
  clearDropMarks();
  if (!li || li.dataset.uid === dragUID) return;

  const list = (orderData && orderData.order) || [];
  const from = list.findIndex(x => x.uid === dragUID);
  const targetIdx = list.findIndex(x => x.uid === li.dataset.uid);
  if (from < 0 || targetIdx < 0) return;

  const r = li.getBoundingClientRect();
  const dropAfter = (ev.clientY - r.top) > r.height / 2;

  // 换算落点到服务端索引：先把被拖行从列表里抽走，再看它该插到剩下的哪一位。
  // 这样做的好处是**不需要**按 from/targetIdx 的相对位置分四种情况讨论——
  // 那些分支极易写错（差 1 的 off-by-one），而"抽走后重排"是自解释的。
  const without = list.filter(x => x.uid !== dragUID);
  const anchorIdx = without.findIndex(x => x.uid === li.dataset.uid);
  const to = dropAfter ? anchorIdx + 1 : anchorIdx;
  if (to === from) return; // 位置没变，无需打扰服务端

  const uid = dragUID;
  dragUID = null;
  try {
    const d = await api('pool/order', { method: 'POST', body: JSON.stringify({ uid, to }) });
    orderData = Object.assign({}, orderData, d);
    await loadOrder();
    if (d.message) toast(d.message, 'ok');
  } catch (e) {
    toast(e.message, 'err');
    await loadOrder();
  }
});

// clearDropMarks 清除所有落点指示线。
function clearDropMarks() {
  document.querySelectorAll('#orderList li.drop-before, #orderList li.drop-after')
    .forEach(li => li.classList.remove('drop-before', 'drop-after'));
}

// 策略切换。
$('orderStrategy').addEventListener('change', async ev => {
  const el = ev.target.closest('input[name="orderStrat"]');
  if (!el) return;
  try {
    const d = await api('pool/strategy', { method: 'POST', body: JSON.stringify({ strategy: el.value }) });
    const label = (orderData && (orderData.strategies || []).find(s => s.id === d.strategy) || {}).label || d.strategy;
    if (d.persist_error) {
      toast('已切换为「' + label + '」（本次生效），但写回配置失败：' + d.persist_error, 'err');
    } else {
      toast('选号策略已切换为「' + label + '」', 'ok');
    }
    await loadOrder();
  } catch (e) { toast(e.message, 'err'); await loadOrder(); }
});

// 恢复默认顺序。
$('btnOrderReset').onclick = async () => {
  if (!confirm('恢复为默认顺序（按账号 ID 升序）？')) return;
  try {
    await api('pool/order', { method: 'POST', body: JSON.stringify({ reset: true }) });
    toast('已恢复默认顺序', 'ok');
    await loadOrder();
  } catch (e) { toast(e.message, 'err'); }
};

/* ── 日志 ─────────────────────────────────────────────────────────── */
async function loadLogs() {
  const box = $('logBox');
  const atEnd = box.scrollTop + box.clientHeight >= box.scrollHeight - 24;
  try {
    const d = await api('logs');
    const lines = d.lines || [];
    box.innerHTML = lines.length
      ? lines.map(l => '<span class="ln' + (/error|失败|错误/.test(l) ? ' e' : /warn|冷却|熔断/.test(l) ? ' w' : '') + '">' + esc(l) + '</span>').join('')
      : '<span style="color:var(--ink-3)">暂无日志</span>';
    if (logPin && atEnd) box.scrollTop = box.scrollHeight;
  } catch (e) { /* 概览已提示 */ }
}
$('btnLogPin').onclick = () => {
  logPin = !logPin;
  $('btnLogPin').textContent = '自动滚动：' + (logPin ? '开' : '关');
};

/* ── 配置 ─────────────────────────────────────────────────────────── */
const CFG_MAP = {
  listen: ['listen'], api_key: ['api_key'],
  checkin_hours: ['schedule', 'checkin_hours'], checkin_enabled: ['schedule', 'checkin_enabled'],
  travel_hours: ['schedule', 'travel_hours'], travel_enabled: ['schedule', 'travel_enabled'],
  activity_hours: ['schedule', 'activity_hours'], activity_enabled: ['schedule', 'activity_enabled'],
  keepalive_hours: ['schedule', 'keepalive_hours'], keepalive_enabled: ['schedule', 'keepalive_enabled'],
  balance_refresh_enabled: ['schedule', 'balance_refresh_enabled'], balance_refresh_minutes: ['schedule', 'balance_refresh_minutes'],
  max_body_mb: ['server', 'max_body_mb'],
  max_in_flight: ['pool', 'max_in_flight'], breaker_threshold: ['pool', 'breaker_threshold'],
  soft_rate: ['cooldown', 'soft_rate'], soft_rate_max: ['cooldown', 'soft_rate_max'],
  breaker_cooldown: ['pool', 'breaker_cooldown'], breaker_cooldown_max: ['pool', 'breaker_cooldown_max'],
  idle_weight_per_hour: ['pool', 'idle_weight_per_hour'], idle_weight_max: ['pool', 'idle_weight_max'],
  ttl: ['session_sticky', 'ttl'],
  timeout_seconds: ['upstream', 'timeout_seconds'], header_timeout_seconds: ['upstream', 'header_timeout_seconds'],
  idle_timeout_seconds: ['upstream', 'idle_timeout_seconds'], user_agent: ['upstream', 'user_agent'],
  prompt_mode: ['prompt', 'mode'], prompt_file: ['prompt', 'file'],
  sanitize_blacklist_fingerprints: ['features', 'sanitize_blacklist_fingerprints'],
  session_sticky_enabled: ['session_sticky', 'enabled'],
};
function dig(obj, path) { return path.reduce((o, k) => (o == null ? undefined : o[k]), obj); }
function put(obj, path, val) {
  let o = obj;
  for (let i = 0; i < path.length - 1; i++) { if (typeof o[path[i]] !== 'object' || o[path[i]] === null) o[path[i]] = {}; o = o[path[i]]; }
  o[path[path.length - 1]] = val;
}

async function loadConfig() {
  try {
    const d = await api('config');
    cfgLoaded = d.config;
    $('cfgPath').textContent = d.path || '';
    const f = $('cfgForm');
    for (const [name, path] of Object.entries(CFG_MAP)) {
      const el = f.elements[name];
      if (!el) continue;
      const v = dig(cfgLoaded, path);
      if (el.type === 'checkbox') el.checked = !!v;
      else if (Array.isArray(v)) el.value = v.join(', ');
      else el.value = v == null ? '' : v;
    }
    $('cfgNote').textContent = '';
    loadRegionTable();
  } catch (e) { toast('读取配置失败：' + e.message, 'err'); }
}

// loadRegionTable 渲染「上游站点」表：两站的域名/登录方式/能力边界与池内账号数。
// 这是用户核对"国际站到底走哪个域名"的唯一界面，故参数全部直出而非摘要。
async function loadRegionTable() {
  const tb = $('rgBody');
  if (!tb) return;
  try {
    const d = await api('regions');
    const counts = d.counts || [];
    const nOf = r => { const c = counts.find(x => x.region === r); return c ? c.total : 0; };
    tb.innerHTML = (d.regions || []).map(r => {
      const growth = r.growth
        ? '<span class="tag ok">支持</span>'
        : '<span class="tag mute">无（任务/签到/旅行 N/A）</span>';
      const models = r.models_api
        ? '<span class="tag ok">支持</span>'
        : '<span class="tag mute">无（模型页 N/A）</span>';
      return '<tr><td class="mark" aria-hidden="true"><i></i></td>' +
        '<td class="who"><div class="nm">' + esc(r.label) + '</div><div class="id">' + esc(r.region) + '</div></td>' +
        '<td class="id" style="font-family:var(--mono);font-size:12px">' + esc(String(r.chat_base).replace(/^https:\/\//, '')) + '</td>' +
        '<td style="font-size:12.5px">' + esc(r.login_hint || '') + '</td>' +
        '<td class="num">' + esc(r.platform) + '</td>' +
        '<td>' + growth + '</td>' +
        '<td>' + models + '</td>' +
        '<td class="num">' + nOf(r.region) + '</td></tr>';
    }).join('');
  } catch (e) {
    tb.innerHTML = '<tr><td colspan="7"><div class="empty">' + esc(e.message) + '</div></td></tr>';
  }
}
function collectConfig() {
  const f = $('cfgForm'), out = {};
  for (const [name, path] of Object.entries(CFG_MAP)) {
    const el = f.elements[name];
    if (!el) continue;
    let v;
    if (el.type === 'checkbox') v = el.checked;
    else if (el.type === 'number') { v = el.value.trim() === '' ? undefined : Number(el.value); }
    else {
      const raw = el.value.trim();
      if (raw === '') v = undefined;
      else if (name.endsWith('_hours')) v = raw.split(/[,，\s]+/).filter(Boolean).map(Number);
      else v = raw;
    }
    if (v !== undefined) put(out, path, v);
  }
  return out;
}
$('btnEye').onclick = () => {
  const el = $('cfgKey');
  const show = el.type === 'password';
  el.type = show ? 'text' : 'password';
  $('btnEye').textContent = show ? '隐藏' : '显示';
};
// 复制 API 密钥。
//
// 取输入框的**实际值**而非后端已保存的值：用户改了密钥还没保存时，
// 复制的应是眼前看到的那串（所见即所得），否则复制到旧密钥会让人困惑
// —— 尤其在"改完密钥准备贴进客户端"这个最高频的使用场景里。
// 密码框的 .value 本身就是明文（type=password 只影响显示），无需先点「显示」。
$('btnCopyKey').onclick = () => {
  const v = $('cfgKey').value;
  if (!v) { toast('密钥为空（留空表示不鉴权），无可复制', 'err'); return; }
  copyText(v, $('btnCopyKey'));
};
$('btnCfgReload').onclick = loadConfig;
$('cfgForm').onsubmit = async ev => {
  ev.preventDefault();
  const btn = $('btnCfgSave');
  btn.disabled = true; btn.textContent = '保存中…';
  try {
    const r = await api('config', { method: 'POST', body: JSON.stringify(collectConfig()) });
    const n = (r.restart_required || []).length;
    toast(n ? '配置已保存，其中 ' + n + ' 项需重启进程生效' : '配置已保存并立即生效', 'ok');
    // 密钥可能已改：本次会话沿用新值，避免下一次轮询被 401。
    const k = $('cfgKey').value.trim();
    if (k) localStorage.setItem(LS_KEY, k);
    loadConfig();
    loadOverview(true);
  } catch (e) { toast('保存失败：' + e.message, 'err'); }
  finally { btn.disabled = false; btn.textContent = '保存配置'; }
};

/* ── 添加账号 ─────────────────────────────────────────────────────── */
// 当前选中的站点（"cn" | "intl"）；由弹窗单选卡片决定，随登录流程透传给后端。
let addRegion = 'cn';
function selectedAddRegion() {
  const el = document.querySelector('input[name="addRegion"]:checked');
  return el ? el.value : 'cn';
}
function openAdd() {
  $('addVeil').classList.add('on');
  $('addPick').hidden = false;
  $('addLoad').hidden = true; $('addReady').hidden = true;
  $('addDone').hidden = true; $('addErr').hidden = true;
  $('btnCopyUrl').hidden = true; $('btnOpenUrl').hidden = true;
  $('btnStartLogin').hidden = false;
  stopPoll();
  // 站点由用户先选，再点「获取授权链接」——国际站与国内站的端点/平台参数不同，
  // 必须先把站点确定下来才能向后端索取对应的授权 URL。
  $('btnCloseAdd').textContent = '取消';
}
// startLogin 按当前选中站点发起授权。
async function startLogin() {
  addRegion = selectedAddRegion();
  const meta = regionMeta[addRegion] || {};
  $('addPick').hidden = true;
  $('addLoad').hidden = false; $('addLoad').innerHTML = '<span class="dots">正在获取授权链接</span>';
  $('addReady').hidden = true; $('addDone').hidden = true; $('addErr').hidden = true;
  stopPoll();
  try {
    const r = await api('login/start?region=' + encodeURIComponent(addRegion), { method: 'POST' });
    loginState = r.state;
    $('addUrl').textContent = r.url;
    $('addLoad').hidden = true; $('addReady').hidden = false;
    $('btnStartLogin').hidden = true;
    $('addHint').textContent = (r.region_label ? r.region_label + '：' : '') +
      (meta.login_hint || '在浏览器打开以下链接并登录：');
    // 国际站需在浏览器内完成邮箱/验证码/SSO 登录，等待窗口更长（15 分钟）；
    // 明确告知避免用户以为卡住而反复重开弹窗。
    const ttl = r.login_ttl_sec || 300;
    $('addPoll').innerHTML = '<span class="dots">等待授权完成，自动检测中</span>' +
      '<span style="color:var(--ink-3);font-size:12px">（最长等待 ' + Math.round(ttl / 60) + ' 分钟）</span>';
    $('btnCopyUrl').hidden = false; $('btnOpenUrl').hidden = false;
    loginTimer = setInterval(pollLogin, 3000);
  } catch (e) {
    $('addLoad').hidden = true;
    $('addErr').hidden = false;
    $('addErr').textContent = e.message;
    $('addPick').hidden = false;
    $('btnStartLogin').hidden = false;
  }
}
function stopPoll() { if (loginTimer) { clearInterval(loginTimer); loginTimer = null; } }
async function pollLogin() {
  if (!loginState) return;
  try {
    const r = await api('login/poll?state=' + encodeURIComponent(loginState));
    if (r.done) {
      stopPoll();
      $('addReady').hidden = true;
      $('addDone').hidden = false;
      $('addDone').textContent = '已添加 ' + (r.nickname || r.uid) +
        (r.region_label ? ' · ' + r.region_label : '') +
        (r.credits >= 0 ? ' · 积分 ' + r.credits : '') + '，账号已载入池中';
      setTimeout(() => { closeAdd(); loadOverview(true); }, 1600);
    }
  } catch (e) {
    stopPoll();
    $('addReady').hidden = true;
    $('addErr').hidden = false;
    $('addErr').textContent = e.message + '（关闭后重新添加）';
  }
}
function closeAdd() { stopPoll(); loginState = null; addRegion = 'cn'; $('addVeil').classList.remove('on'); }
$('btnCloseAdd').onclick = closeAdd;
$('btnStartLogin').onclick = startLogin;
$('btnOpenUrl').onclick = () => open($('addUrl').textContent, '_blank');
$('btnCopyUrl').onclick = () => navigator.clipboard.writeText($('addUrl').textContent)
  .then(() => toast('链接已复制', 'ok'), () => toast('复制失败，请手动选择复制', 'err'));

// 站点单选变化时即时刷新提示文案（未发起授权前可随时改）。
document.querySelectorAll('input[name="addRegion"]').forEach(el => {
  el.onchange = () => {
    addRegion = selectedAddRegion();
    const meta = regionMeta[addRegion] || {};
    $('addLoad').textContent = meta.login_hint || '';
  };
});

/* ── 顶部动作 ─────────────────────────────────────────────────────── */
$('btnAdd').onclick = openAdd;
$('btnRefresh').onclick = async () => {
  const b = $('btnRefresh');
  b.disabled = true; b.textContent = '刷新中…';
  try {
    await api('balance_all', { method: 'POST' });
    await loadOverview(true);
    toast('余额已从上游刷新', 'ok');
  } catch (e) { toast('刷新失败：' + e.message, 'err'); await loadOverview(true); }
  finally { b.disabled = false; b.textContent = '刷新'; }
  if (view === 'logs') loadLogs();
};

/* ── 轮询 ─────────────────────────────────────────────────────────── */
function refreshVisible() {
  if (view === 'accounts') loadOverview(true);
  else if (view === 'logs') loadLogs();
}
function start() {
  refreshAPIURLs();
  loadRegions();
  loadOverview(true);
  if (refTimer) clearInterval(refTimer);
  refTimer = setInterval(refreshVisible, 5000);
  checkAuthGate();
}
async function checkAuthGate() {
  try { await api('overview'); }
  catch (e) { if (String(e.message).includes('密钥') || String(e.message).includes('api_key')) return; }
}
start();

/* ── 积分任务 ─────────────────────────────────────────────────────── */
let taskUID = null;

// 可自动完成的任务（与后端 autoActions 表一致）：判据为行为事件、可经网关复现。
// 其余任务需在官方客户端内交互，面板只展示指引（行 title 提示）。
// 注意：键含点号（Model_chat_GLM5.2）必须加引号，否则会被解析成属性访问 + 数字字面量。
const AUTO_TASKS = {
  'chat_5': '上报 5 条对话活跃事件（自动补足差额）',
  'first_buddy': '上报解锁 → 同意协议 → 领取第一只 Buddy',
  'Model_chat_GLM5.2': '接受任务 → glm-5.2 真实对话一次 → 对齐模型上报',
  'RichMeow_Chat': '桌面指纹事件链上报（已验证可点亮）',
  'Buddy_App': '上报「进入 Buddy 应用」事件链（已验证可点亮）',
  'Buddy_App_QQ': '上报「进入企鹅教师助手」事件链（已验证可点亮）',
  'automation_1': '上报「定时任务创建」事件（已验证可点亮）',
  'Library_read': '上报「读资料库介绍」事件（已验证可点亮）',
  'template_5': '上报「使用模板创建任务」事件组 ×5（三账号实测点亮）',
  'playbook_prompt': '上报「灵感案例做同款发送 Prompt」事件组（三账号实测点亮）',
  'create_canvas': '上报「设计创意画布创建」事件组（三账号实测点亮，+300 分）',
  'expert_5': '真实专家召唤+使用链 ×5（专家市场+真实 chat，三账号实测点亮）',
  'Expert_team_use_3': '真实专家团召唤+使用链 ×3（三账号实测点亮）',
  'Hp_Appearance': '设置主题 API + 皮肤生效事件（两账号实测点亮）',
  'black_cat': '夜猫子：23:00–08:00 窗口内 glm-5.2 对话补足（窗口外提示等 23 点排程）',
  'Expert_lighthouse': '真实轻量云专家召唤+使用链（真实对话 requestId，两账号实测点亮）',
  'skill_1': '真实对话 + skill_info 技能加载事件（实测点亮）'
};

function openTasks(uid) {
  taskUID = uid;
  $('taskWho').textContent = uid.slice(0, 16);
  $('taskVeil').classList.add('on');
  $('btnTaskReload').hidden = false;
  loadTasks();
}
function closeTasks() { $('taskVeil').classList.remove('on'); taskUID = null; }
$('btnCloseTask').onclick = closeTasks;
$('btnTaskReload').onclick = loadTasks;

// 全部接受：把该账号未接受的任务一次性报名（幂等，跳过已接受/已领取）。
$('btnTaskAcceptAll').onclick = async () => {
  if (!taskUID) return;
  const btn = $('btnTaskAcceptAll');
  btn.disabled = true; btn.textContent = '接受中…';
  try {
    const r = await api('accounts/' + encodeURIComponent(taskUID) + '/tasks/accept_all', { method: 'POST' });
    const n = r.accepted || 0;
    if (r.failed && r.failed.length) {
      toast(`已接受 ${n} 个，${r.failed.length} 个被上游拒绝（可重试）`, 'err');
    } else {
      toast(n ? `已接受 ${n} 个任务` : (r.message || '所有任务均已接受'), 'ok');
    }
  } catch (e) { toast(e.message, 'err'); }
  finally { btn.disabled = false; btn.textContent = '全部接受'; loadTasks(); }
};

// 一键完成全部可自动任务（耗时较长：含真实对话，逐项回读验证）。
$('btnTaskAutoAll').onclick = async () => {
  if (!taskUID) return;
  const btn = $('btnTaskAutoAll');
  if (!confirm('将依次执行：补报对话事件、领取 Buddy、glm-5.2 对话、尝试上报。\n过程约 1-2 分钟（含真实对话），确认继续？')) return;
  btn.disabled = true; btn.textContent = '执行中…';
  try {
    const r = await api('accounts/' + encodeURIComponent(taskUID) + '/tasks/auto_all', { method: 'POST' });
    const okN = (r.results || []).filter(x => x.status === 'done').length;
    const skipN = (r.results || []).filter(x => x.status === 'skipped').length;
    const errN = (r.results || []).filter(x => x.status === 'error').length;
    toast(`执行完成：成功 ${okN} 项，跳过 ${skipN} 项${errN ? '，失败 ' + errN + ' 项' : ''}`, errN ? 'err' : 'ok');
    console.log('auto_all results:', r.results);
  } catch (e) { toast(e.message, 'err'); }
  finally { btn.disabled = false; btn.textContent = '一键完成可自动任务'; loadTasks(); }
};

async function loadTasks() {
  if (!taskUID) return;
  const st = $('taskState'), tb = $('taskTable');
  st.hidden = false;
  st.className = 'state';
  st.innerHTML = '<span class="dots">查询中</span>';
  tb.hidden = true;
  try {
    const d = await api('accounts/' + encodeURIComponent(taskUID) + '/tasks');
    const list = d.tasks || [];
    if (!list.length) {
      st.className = 'state';
      st.textContent = '该账号暂无任务';
      return;
    }
    // 有进度或可领取的排前面，已领取沉底——一眼看到"现在该做什么"。
    list.sort((a, b) => (a.claimed - b.claimed) || (b.claimable - a.claimable) || String(a.task_code).localeCompare(String(b.task_code)));
    $('taskBody').innerHTML = list.map(t => {
      // 进度：current 可能缺失（0 或被上游省略）——用 ?? 兜底，避免渲染成 "undefined / N"
      const cur = t.current ?? 0, tgt = t.target ?? 0;
      const prog = tgt ? cur + ' / ' + tgt : (tgt === 0 && cur > 0 ? String(cur) : '—');
      const parts = [];
      if (t.credit) parts.push('+' + t.credit + ' 分');
      if (t.energy) parts.push('+' + t.energy + ' 能');
      if (t.reward_buddy) parts.push('Buddy');
      const reward = parts.length ? parts.join(' ') : '—';
      const badge = t.claimed ? '<span class="tag ok">已领取</span>'
        : t.claimable ? '<span class="tag warn">可领取</span>'
        : t.locked ? '<span class="tag mute">未解锁</span>'
        : t.accept_status === 'accepted' ? '<span class="tag mute">进行中</span>'
        : '<span class="tag mute">未接受</span>';
      const acted = t.claimed || t.locked ? ''
        : t.claimable ? '<button class="xs primary" data-t="claim" data-c="' + esc(t.task_code) + '">领取</button>'
        : AUTO_TASKS[t.task_code] ? '<button class="xs primary" data-t="auto" data-c="' + esc(t.task_code) + '" title="' + esc(AUTO_TASKS[t.task_code]) + '">一键完成</button>'
        : t.accept_status === 'accepted' ? ''
        : '<button class="xs" data-t="accept" data-c="' + esc(t.task_code) + '">接受</button>';
      // 操作指引（description/task_desc）挂 title 提示：如何完成交给用户看
      const tip = [t.title, t.task_desc || t.description, t.jump_url ? '跳转：' + t.jump_url : ''].filter(Boolean).join('\n');
      return '<tr title="' + esc(tip) + '"><td class="mark" aria-hidden="true"><i></i></td>' +
        '<td class="who"><div class="nm">' + esc(t.title || t.task_code) + '</div><div class="id">' + esc(t.task_code) + (t.tag ? ' · ' + esc(t.tag) : '') + '</div></td>' +
        '<td class="num">' + esc(prog) + '</td>' +
        '<td class="num">' + esc(reward) + '</td>' +
        '<td>' + badge + '</td>' +
        '<td class="acts">' + acted + '</td></tr>';
    }).join('');
    st.hidden = true;
    tb.hidden = false;
  } catch (e) {
    st.className = 'state err';
    st.textContent = e.message;
  }
}

$('taskBody').addEventListener('click', async ev => {
  const b = ev.target.closest('button[data-t]');
  if (!b || !taskUID) return;
  const kind = b.dataset.t, code = b.dataset.c;
  b.disabled = true;
  try {
    if (kind === 'auto') {
      // 一键完成：后端执行动作 → 回读进度 → 汇报（耗时可到分钟级，含真实对话）
      b.textContent = '执行中…';
      const r = await api('accounts/' + encodeURIComponent(taskUID) + '/tasks/auto', {
        method: 'POST', body: JSON.stringify({ task_code: code })
      });
      if (r.skipped) {
        toast(r.message || '已跳过', 'ok');
      } else {
        const advanced = r.progress_before !== r.progress_after;
        let msg = r.message || '已执行';
        if (r.progress_after) msg += `（进度 ${r.progress_before} → ${r.progress_after}）`;
        if (r.claimed) msg += '，奖励已自动到账';
        else if (r.claimable) msg += r.claim_error ? '，可点「领取」重试' : '';
        else if (r.attempt && !advanced) msg += '；进度未动，该任务可能需要官方客户端';
        toast(msg, (r.claimed || advanced) ? 'ok' : 'err');
      }
      loadOverview(true);
    } else {
      const path = 'accounts/' + encodeURIComponent(taskUID) + '/tasks/' + (kind === 'claim' ? 'claim' : 'accept');
      const body = kind === 'claim' ? { task_code: code } : { task_codes: [code] };
      await api(path, { method: 'POST', body: JSON.stringify(body) });
      toast(kind === 'claim' ? '已领取奖励' : '已接受任务', 'ok');
      if (kind === 'claim') loadOverview(true);
    }
  } catch (e) { toast(e.message, 'err'); }
  finally { loadTasks(); }
});
