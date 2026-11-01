// 简单播放列表：依次播放容器内各项目的媒体（视频用 <video>，音频用 <audio>）。
import { h } from "./dom.js";
import { icon } from "./ui.js";

// items: [{ title, assetId, hasVideo, fileName }]
export function openPlaylist(items) {
  if (!items || !items.length) return;
  let idx = 0;
  const mediaHost = h("div", { class: "player-host" });
  const titleEl = h("div", { class: "player-title" });
  const list = h("div", { class: "player-list" });

  const btnPrev = h("button", { class: "btn btn-ghost btn-sm", type: "button", html: icon("refresh", 15) + " 上一个" });
  const btnNext = h("button", { class: "btn btn-ghost btn-sm", type: "button", html: "下一个 " + icon("refresh", 15) });
  const playAll = h("button", { class: "btn btn-primary btn-sm", type: "button", html: icon("play", 14) + " 顺序播放" });

  let modalClose = null;

  function renderItem(i) {
    const it = items[i];
    titleEl.textContent = `${i + 1}/${items.length} · ${it.title}`;
    mediaHost.replaceChildren();
    const el = h(it.hasVideo ? "video" : "audio", {
      class: "player-media",
      src: `/api/assets/${it.assetId}/media`,
      controls: true,
      autoplay: true,
    });
    el.addEventListener("ended", () => { if (idx < items.length - 1) { idx++; renderItem(idx); } });
    mediaHost.appendChild(el);
    el.play?.().catch(() => {});
    [...list.children].forEach((c, k) => c.classList.toggle("active", k === i));
  }

  list.replaceChildren(...items.map((it, i) => {
    const row = h("button", { class: "player-row" + (i === 0 ? " active" : ""), type: "button" },
      h("span", { class: "player-row-ic", html: icon(it.hasVideo ? "film" : "audio", 15) }),
      h("span", { class: "player-row-name" }, it.title),
    );
    row.addEventListener("click", () => { idx = i; renderItem(idx); });
    return row;
  }));

  btnPrev.addEventListener("click", () => { if (idx > 0) { idx--; renderItem(idx); } });
  btnNext.addEventListener("click", () => { if (idx < items.length - 1) { idx++; renderItem(idx); } });
  playAll.addEventListener("click", () => renderItem(idx));

  const content = h("div", { class: "player-wrap" },
    mediaHost,
    h("div", { class: "player-bar" }, titleEl, h("span", { class: "spacer" }), btnPrev, playAll, btnNext),
    list,
  );

  import("./ui.js").then(({ openModal }) => {
    const m = openModal({ title: `播放列表 · ${items.length} 项`, wide: true, content });
    modalClose = m.close;
    renderItem(0);
  });
}
