// 通用界面能力：轻提示、确认框、图标、格式化、主题。
import { h } from "./dom.js";
import { post } from "./api.js";

// ---------- 图标 ----------
const ICONS = {
  check: '<path d="M20 6L9 17l-5-5" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"/>',
  alert: '<path d="M12 9v4m0 4h.01M10.3 3.9L2.6 17a2 2 0 001.7 3h15.4a2 2 0 001.7-3L13.7 3.9a2 2 0 00-3.4 0z" fill="none" stroke="currentColor" stroke-width="1.8"/>',
  info: '<circle cx="12" cy="12" r="9" fill="none" stroke="currentColor" stroke-width="1.8"/><path d="M12 11v5m0-8h.01" stroke="currentColor" stroke-width="2" stroke-linecap="round"/>',
  x: '<path d="M6 6l12 12M18 6L6 18" stroke="currentColor" stroke-width="2" stroke-linecap="round"/>',
  play: '<path d="M8 5l11 7-11 7z" fill="currentColor"/>',
  pause: '<path d="M7 5h4v14H7zM13 5h4v14h-4z" fill="currentColor"/>',
  upload: '<path d="M12 16V4m0 0L7 9m5-5l5 5M4 17v2a1 1 0 001 1h14a1 1 0 001-1v-2" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/>',
  download: '<path d="M12 4v12m0 0l-5-5m5 5l5-5M4 17v2a1 1 0 001 1h14a1 1 0 001-1v-2" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/>',
  trash: '<path d="M4 7h16M9 7V5h6v2m-8 0l1 13h8l1-13" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"/>',
  edit: '<path d="M4 20h4L19 9l-4-4L4 16v4z" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linejoin="round"/>',
  film: '<rect x="3" y="5" width="18" height="14" rx="2" fill="none" stroke="currentColor" stroke-width="1.7"/><path d="M7 5v14M17 5v14M3 10h4M3 14h4M17 10h4M17 14h4" stroke="currentColor" stroke-width="1.4"/>',
  audio: '<path d="M4 10v4m4-7v10m4-13v16m4-11v6m4-4v2" stroke="currentColor" stroke-width="2" stroke-linecap="round"/>',
  more: '<circle cx="12" cy="5" r="1.6" fill="currentColor"/><circle cx="12" cy="12" r="1.6" fill="currentColor"/><circle cx="12" cy="19" r="1.6" fill="currentColor"/>',
  refresh: '<path d="M20 11a8 8 0 10-2.5 6M20 4v7h-7" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/>',
  folder: '<path d="M10 4H4a2 2 0 00-2 2v12a2 2 0 002 2h16a2 2 0 002-2V8a2 2 0 00-2-2h-8l-2-2z" fill="none" stroke="currentColor" stroke-width="1.7"/>',
  globe: '<circle cx="12" cy="12" r="9" fill="none" stroke="currentColor" stroke-width="1.7"/><path d="M3 12h18M12 3c2.5 2.5 3.8 5.6 3.8 9S14.5 18.5 12 21c-2.5-2.5-3.8-5.6-3.8-9S9.5 5.5 12 3z" fill="none" stroke="currentColor" stroke-width="1.4"/>',
  sparkles: '<path d="M12 3l1.8 4.7L18.5 9.5l-4.7 1.8L12 16l-1.8-4.7L5.5 9.5l4.7-1.8L12 3zM18 15l.9 2.1 2.1.9-2.1.9L18 21l-.9-2.1-2.1-.9 2.1-.9L18 15z" fill="currentColor"/>',
  archive: '<rect x="3" y="4" width="18" height="5" rx="1.4" fill="none" stroke="currentColor" stroke-width="1.7"/><path d="M5 9v10a1 1 0 001 1h12a1 1 0 001-1V9M10 13h4" fill="none" stroke="currentColor" stroke-width="1.7"/>',
  monitor: '<rect x="3" y="4" width="18" height="12" rx="1.8" fill="none" stroke="currentColor" stroke-width="1.7"/><path d="M9 20h6M12 16v4" stroke="currentColor" stroke-width="1.7" stroke-linecap="round"/>',
  shuffle: '<path d="M4 7h4l8 10h4M4 17h4l3-3.6M16 7h4m0 0l-2.5-2.5M20 7l-2.5 2.5M16 17h4m0 0l-2.5-2.5M20 17l-2.5 2.5" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"/>',
  search: '<circle cx="11" cy="11" r="6.5" fill="none" stroke="currentColor" stroke-width="1.8"/><path d="M16 16l4.5 4.5" stroke="currentColor" stroke-width="1.9" stroke-linecap="round"/>',
  wave: '<path d="M3 12h2l2-5 3 10 3-14 3 12 2-3h3" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"/>',
  collection: '<path d="M12 3l9 5-9 5-9-5 9-5z" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linejoin="round"/><path d="M3 13l9 5 9-5" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"/>',
  grid: '<rect x="4" y="4" width="7" height="7" rx="1.4" fill="none" stroke="currentColor" stroke-width="1.7"/><rect x="13" y="4" width="7" height="7" rx="1.4" fill="none" stroke="currentColor" stroke-width="1.7"/><rect x="4" y="13" width="7" height="7" rx="1.4" fill="none" stroke="currentColor" stroke-width="1.7"/><rect x="13" y="13" width="7" height="7" rx="1.4" fill="none" stroke="currentColor" stroke-width="1.7"/>',
  arrowLeft: '<path d="M15 5l-7 7 7 7" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/>',
  arrowRight: '<path d="M9 5l7 7-7 7" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/>',
  arrowUp: '<path d="M5 15l7-7 7 7" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/>',
  scissors: '<circle cx="6" cy="6" r="2.4" fill="none" stroke="currentColor" stroke-width="1.6"/><circle cx="6" cy="18" r="2.4" fill="none" stroke="currentColor" stroke-width="1.6"/><path d="M8 7.5L20 18M20 6L8 16.5" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"/>',
  copy: '<rect x="9" y="9" width="11" height="11" rx="2" fill="none" stroke="currentColor" stroke-width="1.7"/><path d="M15 6.5A2.5 2.5 0 0012.5 4H6a2 2 0 00-2 2v6.5A2.5 2.5 0 006.5 15" fill="none" stroke="currentColor" stroke-width="1.7"/>',
  paste: '<path d="M9 4h6v3H9zM7 5H5v15h14V5h-2" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linejoin="round"/><path d="M9 12h6M9 16h4" stroke="currentColor" stroke-width="1.7" stroke-linecap="round"/>',
};

export function icon(name, size = 18) {
  const body = ICONS[name] || ICONS.info;
  return `<svg viewBox="0 0 24 24" width="${size}" height="${size}" aria-hidden="true">${body}</svg>`;
}

// ---------- 轻提示 ----------
let toastHost = null;

export function toast(title, message = "", type = "info", timeout = 4200) {
  if (!toastHost) toastHost = document.getElementById("toasts");
  if (!toastHost) return;
  const tone = { success: "check", error: "alert", warn: "alert", info: "info" }[type] || "info";
  const node = h("div", { class: `toast toast-${type}`, role: "status" },
    h("span", { class: "ic", html: icon(tone, 18) }),
    h("div", { class: "content" },
      h("strong", {}, title),
      message ? h("span", {}, message) : null,
    ),
    h("button", { class: "icon-btn", style: "width:24px;height:24px", "aria-label": "关闭", html: icon("x", 14) })
  );
  node.querySelector("button").addEventListener("click", () => dismiss());
  toastHost.appendChild(node);
  let timer = timeout > 0 ? setTimeout(dismiss, timeout) : null;
  function dismiss() {
    if (timer) clearTimeout(timer);
    node.classList.add("leaving");
    setTimeout(() => node.remove(), 180);
  }
  return { dismiss, node };
}

// ---------- 确认框 ----------
export function confirmDialog({ title, message, confirmText = "确定", cancelText = "取消", danger = false }) {
  return new Promise((resolve) => {
    const root = document.getElementById("modal-root");
    const modal = h("div", { class: "modal", role: "dialog", "aria-modal": "true" },
      h("div", { class: "modal-head" }, h("h3", {}, title)),
      h("div", { class: "modal-body" }, h("p", { style: "margin:0" }, message)),
      h("div", { class: "modal-foot" },
        h("button", { class: "btn btn-ghost", type: "button" }, cancelText),
        h("button", { class: `btn ${danger ? "btn-danger" : "btn-primary"}`, type: "button" }, confirmText),
      )
    );
    root.replaceChildren(modal);
    root.hidden = false;
    const [cancelBtn, okBtn] = modal.querySelectorAll(".modal-foot button");
    const close = (value) => {
      root.hidden = true;
      root.replaceChildren();
      document.removeEventListener("keydown", onKey);
      resolve(value);
    };
    const onKey = (ev) => { if (ev.key === "Escape") close(false); };
    cancelBtn.addEventListener("click", () => close(false));
    okBtn.addEventListener("click", () => close(true));
    root.addEventListener("click", (ev) => { if (ev.target === root) close(false); }, { once: true });
    document.addEventListener("keydown", onKey);
    okBtn.focus();
  });
}

// ---------- 模态框（自定义内容） ----------
export function openModal({ title, content, footer, wide = false, onClose }) {
  const root = document.getElementById("modal-root");
  const modal = h("div", { class: `modal${wide ? " wide" : ""}`, role: "dialog", "aria-modal": "true" },
    h("div", { class: "modal-head" },
      h("h3", {}, title),
      h("button", { class: "icon-btn", type: "button", "aria-label": "关闭", html: icon("x", 16) }),
    ),
    h("div", { class: "modal-body" }, content),
    footer ? h("div", { class: "modal-foot" }, footer) : null,
  );
  root.replaceChildren(modal);
  root.hidden = false;
  const close = () => {
    root.hidden = true;
    root.replaceChildren();
    document.removeEventListener("keydown", onKey);
    onClose?.();
  };
  const onKey = (ev) => { if (ev.key === "Escape") close(); };
  modal.querySelector(".modal-head .icon-btn").addEventListener("click", close);
  root.addEventListener("click", (ev) => { if (ev.target === root) close(); });
  document.addEventListener("keydown", onKey);
  return { close, element: modal };
}

// ---------- 右键/下拉菜单 ----------
export function menuPopup(x, y, items) {
  const root = document.getElementById("modal-root");
  const menu = h("div", { class: "ctx-menu" },
    ...items.map((it) => {
      if (it.separator) return h("div", { class: "ctx-sep" });
      const el = h("button", {
        class: "ctx-item" + (it.danger ? " danger" : "") + (it.disabled ? " disabled" : ""),
        type: "button",
      }, it.label);
      if (it.icon) el.prepend(h("span", { class: "ctx-ic", html: icon(it.icon, 15) }));
      // 与资源管理器一致：右侧显示快捷键提示。
      if (it.shortcut) el.append(h("span", { class: "ctx-shortcut" }, it.shortcut));
      if (!it.disabled) {
        el.addEventListener("click", () => { close(); it.onClick?.(); });
      }
      return el;
    })
  );
  root.replaceChildren(menu);
  root.hidden = false;
  requestAnimationFrame(() => {
    const mw = menu.offsetWidth, mh = menu.offsetHeight;
    menu.style.left = Math.max(8, Math.min(x, window.innerWidth - mw - 8)) + "px";
    menu.style.top = Math.max(8, Math.min(y, window.innerHeight - mh - 8)) + "px";
  });
  function close() {
    root.hidden = true;
    root.replaceChildren();
    document.removeEventListener("click", onDoc, true);
    document.removeEventListener("keydown", onKey);
  }
  const onDoc = (ev) => { if (!menu.contains(ev.target)) close(); };
  const onKey = (ev) => { if (ev.key === "Escape") close(); };
  setTimeout(() => document.addEventListener("click", onDoc, true), 0);
  document.addEventListener("keydown", onKey);
  return { close };
}

// ---------- 格式化 ----------
export function bytes(n) {
  if (!n || n < 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let v = Number(n);
  let i = 0;
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i += 1; }
  return `${v >= 100 || i === 0 ? Math.round(v) : v.toFixed(1)} ${units[i]}`;
}

export function duration(seconds) {
  if (!seconds || seconds < 0 || !isFinite(seconds)) return "0:00";
  const total = Math.round(seconds);
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  if (h > 0) return `${h}:${String(m).padStart(2, "0")}:${String(s).padStart(2, "0")}`;
  return `${m}:${String(s).padStart(2, "0")}`;
}

export function timecode(seconds, dot = false) {
  const total = Math.max(0, Math.round((Number(seconds) || 0) * 1000));
  const h = Math.floor(total / 3600000);
  const m = Math.floor((total % 3600000) / 60000);
  const s = Math.floor((total % 60000) / 1000);
  const ms = total % 1000;
  return `${String(h).padStart(2, "0")}:${String(m).padStart(2, "0")}:${String(s).padStart(2, "0")}${dot ? "." : ","}${String(ms).padStart(3, "0")}`;
}

export function parseTimecode(value) {
  const text = String(value || "").trim().replace(",", ".");
  if (!text) return NaN;
  if (!text.includes(":")) return parseFloat(text);
  const parts = text.split(":").map((p) => parseFloat(p));
  if (parts.some((p) => !isFinite(p))) return NaN;
  let secs = 0;
  for (const p of parts) secs = secs * 60 + p;
  return secs;
}

export function relTime(iso) {
  if (!iso) return "";
  const then = new Date(iso).getTime();
  if (!isFinite(then)) return "";
  const diff = Date.now() - then;
  if (diff < 60_000) return "刚刚";
  if (diff < 3600_000) return `${Math.floor(diff / 60_000)} 分钟前`;
  if (diff < 86400_000) return `${Math.floor(diff / 3600_000)} 小时前`;
  if (diff < 7 * 86400_000) return `${Math.floor(diff / 86400_000)} 天前`;
  return new Date(iso).toLocaleDateString("zh-CN");
}

export function dateTime(iso) {
  if (!iso) return "";
  const d = new Date(iso);
  if (!isFinite(d.getTime())) return "";
  return d.toLocaleString("zh-CN", { year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit" });
}

export function resolution(probe) {
  if (!probe || !probe.width) return "";
  const h = probe.height;
  const label = h >= 2160 ? "4K" : h >= 1440 ? "2K" : h >= 1080 ? "1080p" : h >= 720 ? "720p" : h >= 480 ? "480p" : `${h}p`;
  return `${probe.width}×${h} · ${label}`;
}

// 简易转义（供模板字符串内联使用）
export function escHtml(value) {
  return String(value ?? "")
    .replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;").replace(/'/g, "&#39;");
}

// ---------- 主题 ----------
const THEME_KEY = "av-workbench-theme";

export function initTheme(initial = "auto") {
  applyTheme(localStorage.getItem(THEME_KEY) || initial);
  document.getElementById("theme-toggle")?.addEventListener("click", () => {
    const current = localStorage.getItem(THEME_KEY) || "auto";
    const order = ["auto", "light", "dark"];
    const next = order[(order.indexOf(current) + 1) % order.length];
    applyTheme(next);
    toast("外观已切换", { auto: "跟随系统", light: "浅色", dark: "深色" }[next], "info", 1800);
    post("/api/settings", { ...(window.__settings || {}), theme: next }).catch(() => {});
    window.__settings = { ...(window.__settings || {}), theme: next };
  });
}

export function applyTheme(mode) {
  const root = document.documentElement;
  const resolved = mode === "auto"
    ? (window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light")
    : mode;
  root.dataset.theme = resolved;
  localStorage.setItem(THEME_KEY, mode);
}

// ---------- 进度环 ----------
export function progressRing(pct, size = 54, stroke = 4) {
  const r = (size - stroke) / 2;
  const c = 2 * Math.PI * r;
  const clamped = Math.max(0, Math.min(1, pct || 0));
  return `<svg width="${size}" height="${size}" viewBox="0 0 ${size} ${size}">
    <circle cx="${size / 2}" cy="${size / 2}" r="${r}" fill="none" stroke="var(--surface-2)" stroke-width="${stroke}"/>
    <circle cx="${size / 2}" cy="${size / 2}" r="${r}" fill="none" stroke="var(--accent)" stroke-width="${stroke}"
      stroke-linecap="round" stroke-dasharray="${c}" stroke-dashoffset="${c * (1 - clamped)}"
      style="transition:stroke-dashoffset .3s ease"/>
  </svg>`;
}

// ---------- 复选框行 ----------
export function checkbox(id, label, checked) {
  return h("label", { class: "check", for: id },
    h("input", { type: "checkbox", id, checked: Boolean(checked) }),
    h("span", {}, label),
  );
}
