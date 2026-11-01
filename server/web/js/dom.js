// 极简 DOM 工具：用模板字符串构建界面，用事件委托绑定交互。
// 不引入框架，保证首屏与热路径都足够轻。

/** 创建元素：h("div", {class:"x", onclick:fn}, "文本", 子节点) */
export function h(tag, props = {}, ...children) {
  const el = document.createElement(tag);
  if (props) {
    for (const [key, value] of Object.entries(props)) {
      if (value === null || value === undefined || value === false) continue;
      if (key === "class" || key === "className") el.className = value;
      else if (key === "dataset") Object.assign(el.dataset, value);
      else if (key === "style") {
        if (typeof value === "string") el.style.cssText = value;
        else Object.assign(el.style, value);
      } else if (key === "html") el.innerHTML = value;
      else if (key.startsWith("on") && typeof value === "function") {
        el.addEventListener(key.slice(2).toLowerCase(), value);
      } else if (key === "value" || key === "checked" || key === "disabled" || key === "selected") {
        el[key] = value;
      } else el.setAttribute(key, value);
    }
  }
  append(el, children);
  return el;
}

function append(el, children) {
  for (const child of children) {
    if (child === null || child === undefined || child === false) continue;
    if (Array.isArray(child)) append(el, child);
    else if (child instanceof Node) el.appendChild(child);
    else el.appendChild(document.createTextNode(String(child)));
  }
}

/** HTML 转义，用于 innerHTML 模板 */
export function esc(value) {
  return String(value ?? "")
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}

export function $(selector, root = document) {
  return root.querySelector(selector);
}

export function $$(selector, root = document) {
  return Array.from(root.querySelectorAll(selector));
}

/** 把 HTML 字符串转成元素（首元素） */
export function fromHTML(html) {
  const tpl = document.createElement("template");
  tpl.innerHTML = html.trim();
  return tpl.content.firstElementChild;
}

/** 事件委托：在 root 上监听，匹配 selector 时触发 */
export function delegate(root, event, selector, handler) {
  root.addEventListener(event, (ev) => {
    const target = ev.target.closest(selector);
    if (target && root.contains(target)) handler(ev, target);
  });
}

export function clear(el) {
  while (el.firstChild) el.removeChild(el.firstChild);
  return el;
}

export function show(el) { if (el) el.classList.remove("hidden"); }
export function hide(el) { if (el) el.classList.add("hidden"); }

/** 防抖 */
export function debounce(fn, wait = 220) {
  let timer;
  return (...args) => {
    clearTimeout(timer);
    timer = setTimeout(() => fn(...args), wait);
  };
}

/** 节流 */
export function throttle(fn, wait = 100) {
  let last = 0;
  let timer;
  return (...args) => {
    const now = Date.now();
    const remain = wait - (now - last);
    if (remain <= 0) {
      last = now;
      fn(...args);
    } else if (!timer) {
      timer = setTimeout(() => {
        timer = null;
        last = Date.now();
        fn(...args);
      }, remain);
    }
  };
}
