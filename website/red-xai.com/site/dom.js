export function h(tag, props, ...children) {
  const node = document.createElement(tag);
  for (const [name, value] of Object.entries(props || {})) {
    if (value === null || value === undefined || value === false) continue;
    if (name.startsWith('on') && typeof value === 'function') node.addEventListener(name.slice(2).toLowerCase(), value);
    else if (name === 'class') node.className = value;
    else if (name === 'text') node.textContent = value;
    else if (name === 'value') node.value = value;
    else if (name === 'checked') node.checked = value;
    else if (name === 'disabled') node.disabled = value;
    else node.setAttribute(name, value === true ? '' : String(value));
  }
  append(node, children);
  return node;
}
function append(node, children) {
  for (const child of children.flat(Infinity)) {
    if (child === null || child === undefined || child === false) continue;
    node.append(typeof child === 'object' && typeof child.nodeType === 'number' ? child : document.createTextNode(String(child)));
  }
}
export const replace = (node, ...children) => { node.replaceChildren(); append(node, children); return node; };
export const button = (text, onClick, secondary = false) => h('button', { type: 'button', class: 'button' + (secondary ? ' secondary' : ''), onClick }, text);
export const link = (text, href, className = '') => h('a', { href, class: className, ...(href.startsWith('/') && !['/signup','/activate','/tos'].some(p => href === p || href.startsWith(p+'?')) ? { 'data-route': true } : {}) }, text);
export const external = (text, href, className = '') => h('a', { href: safeHref(href), ...(className ? { class: className } : {}), target: '_blank', rel: 'noopener noreferrer' }, text);
export const alert = (message, tone = '') => h('div', { class: 'alert ' + tone, role: 'status' }, message);
export function field(label, input) { const id = input.id || 'field-' + Math.random().toString(36).slice(2); input.id = id; return h('div', { class: 'field' }, h('label', { for: id }, label), input); }
export function safeHref(value) {
  if (typeof value !== 'string' || /[\u0000-\u0020\u007f\\]/u.test(value)) return '#';
  if (value.startsWith('/') && !value.startsWith('//')) return value;
  try { const u = new URL(value); return ['https:','http:'].includes(u.protocol) && !u.username && !u.password ? u.href : '#'; } catch { return '#'; }
}
export function safeNext(value) {
  if (typeof value !== 'string' || value.length > 2048 || /[\u0000-\u0020\u007f\\]/u.test(value) || !value.startsWith('/') || value.startsWith('//')) return '/';
  const path = value.split(/[?#]/u,1)[0];
  if (['/','/db','/host','/settings','/api-keys','/community'].includes(path)
    || ['/products/','/news/','/media/','/brands/','/redxai-connect/','/redxai-connectx/'].some(p => path.startsWith(p))
    || ['/redxai-connect','/redxai-connectx'].includes(path)) return value;
  return '/';
}
export function detectPlatform(nav = {}) {
  const value = String(nav.userAgentData?.platform || nav.platform || nav.userAgent || '').toLowerCase() + ' ' + String(nav.userAgent || '').toLowerCase();
  if (/android|iphone|ipad|ipod/u.test(value)) return 'mobile';
  if (/win/u.test(value)) return 'windows';
  if (/mac/u.test(value)) return 'macos';
  if (/linux|x11/u.test(value)) return 'linux';
  return 'unknown';
}
export function formatDate(value, preferences = {}, withTime = true) {
  if (!value || !Number.isFinite(Date.parse(value))) return 'Date to be announced';
  try { return new Intl.DateTimeFormat(preferences.locale || undefined, { dateStyle: 'medium', ...(withTime ? { timeStyle: 'short' } : {}), ...(preferences.timeZone ? { timeZone: preferences.timeZone } : {}) }).format(new Date(value)); }
  catch { return new Date(value).toLocaleString(); }
}
export function pageHead(eyebrow, title, description, actions) {
  return h('header', { class: 'page-head wrap' }, h('div', { class: 'eyebrow' }, eyebrow), h('h1', null, title), description ? h('p', null, description) : null, actions);
}
export function empty(title, text, action) { return h('section', { class: 'empty' }, h('h2', null, title), h('p', null, text), action); }
export function notify(message) {
  const box = document.getElementById('notice'); if (!box) return;
  box.textContent = message; box.hidden = false; clearTimeout(notify.timer);
  notify.timer = setTimeout(() => { box.hidden = true; }, 6500);
}
export async function copyText(value) {
  try { await navigator.clipboard.writeText(value); notify('Copied.'); }
  catch { notify('Copy is unavailable in this browser. Select and copy the text.'); }
}

// Render only a small, safe Markdown vocabulary. Raw HTML is always text.
export function renderMarkdown(markdown, { documentLinks = new Set(), path = '' } = {}) {
  const root = h('div', { class: 'markdown' });
  const lines = String(markdown || '').split(/\r?\n/u); let code = null, list = null;
  const inline = text => {
    const out = []; const pattern = /(`[^`\n]+`|\*\*[^*\n]+\*\*|\[[^\]\n]+\]\([^\s)]+\))/gu; let previous = 0;
    for (const match of text.matchAll(pattern)) {
      out.push(text.slice(previous, match.index)); const s = match[0];
      if (s.startsWith('`')) out.push(h('code', null, s.slice(1,-1)));
      else if (s.startsWith('**')) out.push(h('strong', null, s.slice(2,-2)));
      else { const m = /^\[([^\]]+)\]\(([^)]+)\)$/u.exec(s); const id = m[2].split(/[\/#]/u).filter(Boolean).at(-1)?.replace(/\.md$/u,'');
        out.push(documentLinks.has(id) ? link(m[1], path+'?doc='+encodeURIComponent(id)) : h('a', { href: safeHref(m[2]), rel: 'noopener noreferrer' }, m[1])); }
      previous = match.index + s.length;
    }
    out.push(text.slice(previous)); return out;
  };
  for (const line of lines) {
    if (/^\s*```/u.test(line)) { list = null; if (code) { root.append(h('pre', null, h('code', null, code.join('\n')))); code = null; } else code = []; continue; }
    if (code) { code.push(line); continue; }
    if (!line.trim()) { list = null; continue; }
    const heading = /^(#{1,4})\s+(.+)$/u.exec(line);
    if (heading) { list = null; root.append(h('h'+Math.min(4,heading[1].length+1), null, inline(heading[2]))); continue; }
    const item = /^\s*([-*]|\d+\.)\s+(.+)$/u.exec(line);
    if (item) { const type = /\d/u.test(item[1]) ? 'ol' : 'ul'; if (!list || list.tagName.toLowerCase() !== type) { list = h(type); root.append(list); } list.append(h('li', null, inline(item[2]))); continue; }
    list = null; if (/^\s*---+\s*$/u.test(line)) root.append(h('hr'));
    else root.append(h('p', null, inline(line)));
  }
  if (code) root.append(h('pre', null, h('code', null, code.join('\n'))));
  return root;
}
