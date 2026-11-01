// 播放页：项目媒体的正式播放界面（视频/音频、字幕叠加、文件信息与常用操作）。
// 入口：#/player/<projectId>[?asset=<outputAssetId>]
import { get } from "../api.js";
import { h } from "../dom.js";
import { icon, toast, bytes, duration, dateTime, escHtml } from "../ui.js";
import { emptyState } from "../components/media.js";
import { state } from "../store.js";
import { pickFile } from "../app.js";

export default {
  async render(root, ctx) {
    let project = null;
    const id = ctx.id || state.activeProject?.id || "";
    if (!id) {
      root.replaceChildren(emptyState({
        iconName: "film",
        title: "还没有可播放的媒体",
        message: "先从项目库打开一个项目，或导入新文件。",
        action: h("button", {
          class: "btn btn-primary btn-sm", type: "button",
          onclick: () => pickFile({ route: "player", temporary: false }),
        }, "导入媒体"),
      }));
      return null;
    }
    try {
      project = (await get(`/api/projects/${id}`)).project;
    } catch (_) {
      root.replaceChildren(emptyState({
        iconName: "film", title: "项目不存在", message: "它可能已被删除。",
        action: h("a", { class: "btn btn-sm btn-primary", href: "#/projects" }, "返回项目库"),
      }));
      return null;
    }

    const assets = collectAssets(project);
    const wanted = ctx.query?.get("asset") || "";
    const current = assets.find((it) => it.asset.id === wanted) || assets[0];

    // 先用 meta 模式判断文件是否真的在磁盘上，避免浏览器里出现裸 JSON 错误。
    let exists = true;
    try {
      const meta = await get(`/api/assets/${current.asset.id}?meta=1`);
      exists = meta.exists !== false;
    } catch (_) {
      exists = false;
    }

    const page = buildPage(project, assets, current, exists);
    root.replaceChildren(page.element);
    page.mount();
    return page.cleanup;
  },
};

// collectAssets 汇总可播放的资源：原片 + 成片 + 附件里的音视频。
function collectAssets(project) {
  const out = [];
  if (project.media) out.push({ label: "原片", asset: project.media, primary: true });
  for (const o of project.outputs || []) {
    if (o) out.push({ label: o.fileName || "成片", asset: o, primary: false });
  }
  for (const at of project.attachments || []) {
    if (!at?.id) continue;
    const media = at.probe?.hasVideo || at.probe?.hasAudio;
    if (media) out.push({ label: at.fileName || "附件", asset: at, primary: false });
  }
  return out;
}

function buildPage(project, assets, current, exists) {
  const asset = current.asset;
  const probe = asset.probe || {};
  const isVideo = Boolean(probe.hasVideo);
  const isPrimary = current.primary;
  const folder = (state.folders || []).find((f) => f.id === project.folderId);

  const element = h("div", { class: "play-page" });

  // ---- 顶部 ----
  const head = h("div", { class: "play-head" });
  head.innerHTML = `
    <a class="icon-btn" href="#/project/${project.id}" title="返回项目">${icon("arrowLeft", 18)}</a>
    <div class="play-head-text">
      <h1>${escHtml(project.title)}</h1>
      <div class="play-head-meta">
        <span>${isVideo ? "视频" : "音频"}</span>
        <span>·</span>
        <span>${duration(project.duration)}</span>
        <span>·</span>
        <span>${escHtml(asset.fileName || "")}</span>
      </div>
    </div>
    <span class="spacer"></span>
    <div class="cluster">
      ${assets.length > 1 ? `<select class="select input-sm play-switch" aria-label="选择要播放的文件"></select>` : ""}
      <a class="btn btn-sm btn-ghost" href="#/project/${project.id}">项目详情</a>
    </div>
  `;
  element.appendChild(head);

  // ---- 主体：播放器 + 侧栏 ----
  const grid = h("div", { class: "play-grid" });
  const stage = h("div", { class: "play-stage" });
  const mediaHost = h("div", { class: "play-media" });
  const subtitleLine = h("div", { class: "play-subtitle", hidden: true });
  const errorBox = h("div", { class: "play-error", hidden: true });
  stage.append(mediaHost, subtitleLine, errorBox);

  let player = null;
  let cues = [];
  let showSubs = true;
  let cleanups = [];

  if (exists) {
    player = h(isVideo ? "video" : "audio", {
      class: "play-el",
      src: `/api/assets/${asset.id}/media`,
      controls: true,
      preload: "metadata",
      playsinline: true,
    });
    if (isVideo && isPrimary) {
      player.poster = `/api/projects/${project.id}/thumbnail?at=1`;
    }
    mediaHost.appendChild(player);
  } else {
    showError(errorBox);
  }

  function showError(box) {
    box.hidden = false;
    box.innerHTML = `
      <div class="play-error-inner">
        <span class="play-error-ic">${icon("alert", 30)}</span>
        <strong>源文件已丢失</strong>
        <p>这个项目的媒体文件不在磁盘上（可能被移动、删除或清理）。可以重新导入，或回到项目库清理这条记录。</p>
        <div class="cluster">
          <button class="btn btn-primary btn-sm" type="button" data-role="reimport">重新导入</button>
          <a class="btn btn-sm btn-ghost" href="#/project/${project.id}">打开项目</a>
          ${isPrimary ? "" : `<a class="btn btn-sm btn-ghost" href="#/player/${project.id}">播放原片</a>`}
        </div>
      </div>
    `;
    box.querySelector("[data-role=reimport]")?.addEventListener("click", () => {
      pickFile({ route: "project", temporary: false, onImported: () => location.reload() });
    });
  }

  // ---- 侧栏 ----
  const side = h("aside", { class: "play-side" });
  const quick = h("div", { class: "card card-pad play-actions" });
  quick.innerHTML = `<h4>继续处理</h4>`;
  const quickGrid = h("div", { class: "play-action-grid" });
  const action = (ic, label, desc, href) => {
    const a = h("a", { class: "play-action", href });
    a.innerHTML = `<span class="play-action-ic">${icon(ic, 16)}</span>
      <span><strong>${label}</strong><span>${desc}</span></span>`;
    return a;
  };
  if (isPrimary) {
    quickGrid.append(
      action("wave", "字幕转写", "识别语音生成字幕", `#/transcribe/${project.id}`),
      action("shuffle", "视频转码", "改格式、压体积", `#/transcode/${project.id}`),
      action("search", "媒体分析", "查看编码与流信息", `#/analyze/${project.id}`),
      action("edit", "字幕编辑", (project.subtitles || []).length ? "校对当前字幕" : "还没有字幕", `#/editor/${project.id}`),
    );
  } else {
    quickGrid.append(
      action("folder", "回到项目", "查看成片与附件", `#/project/${project.id}`),
      action("film", "播放原片", "切换到项目主素材", `#/player/${project.id}`),
    );
  }
  quick.appendChild(quickGrid);

  const info = h("div", { class: "card card-pad play-info" });
  info.innerHTML = `
    <h4>文件信息</h4>
    <div class="play-kv">
      ${kv("文件名", asset.fileName || "—")}
      ${kv("大小", bytes(asset.size || 0))}
      ${kv("时长", duration(probe.duration || project.duration))}
      ${isVideo ? kv("分辨率", probe.width ? `${probe.width}×${probe.height}` : "—") : ""}
      ${isVideo ? kv("帧率", probe.frameRate ? probe.frameRate.toFixed(2) + " fps" : "—") : ""}
      ${kv("视频编码", probe.videoCodec || "—")}
      ${kv("音频编码", probe.audioCodec || "—")}
      ${kv("字幕", `${(project.subtitles || []).length} 份`)}
      ${kv("位置", folder ? folder.name : "未分类")}
      ${kv("修改时间", dateTime(project.updatedAt))}
    </div>
    <div class="cluster" style="margin-top:12px">
      <a class="btn btn-sm btn-ghost" href="/api/assets/${asset.id}/download">下载文件</a>
      ${isPrimary ? `<button class="btn btn-sm btn-ghost" type="button" data-role="open-project">在项目库中显示</button>` : ""}
    </div>
  `;

  side.append(quick, info);

  if (isPrimary && (project.subtitles || []).length) {
    const subCard = h("div", { class: "card card-pad play-subcard" });
    subCard.innerHTML = `
      <div class="cluster">
        <h4 style="margin:0">字幕叠加</h4>
        <span class="spacer"></span>
        <label class="check"><input type="checkbox" checked data-role="subs"> 显示</label>
      </div>
      <p class="small muted" style="margin:8px 0 0">播放时在画面下方显示当前这句字幕。</p>
    `;
    subCard.querySelector("[data-role=subs]").addEventListener("change", (ev) => {
      showSubs = ev.target.checked;
      if (!showSubs) subtitleLine.hidden = true;
    });
    side.appendChild(subCard);
  }

  grid.append(stage, side);
  element.appendChild(grid);

  // ---- 切换原片 / 成片 ----
  const switcher = head.querySelector(".play-switch");
  if (switcher) {
    assets.forEach((it) => {
      switcher.appendChild(h("option", {
        value: it.asset.id, selected: it.asset.id === asset.id,
      }, `${it.label}${it.primary ? "" : " · " + duration(it.asset.probe?.duration || 0)}`));
    });
    switcher.addEventListener("change", () => {
      const target = switcher.value === project.media?.id
        ? `#/player/${project.id}`
        : `#/player/${project.id}?asset=${encodeURIComponent(switcher.value)}`;
      location.hash = target;
    });
  }

  const mount = async () => {
    info.querySelector("[data-role=open-project]")?.addEventListener("click", () => {
      location.hash = `#/project/${project.id}`;
    });

    if (!player) return;

    // 字幕：读取第一份字幕，随播放进度显示。
    if (isPrimary && (project.subtitles || []).length) {
      const track = project.subtitles[0];
      try {
        const data = await get(`/api/projects/${project.id}/exports?format=json&track=${track.id}&inline=1`);
        cues = JSON.parse(data.content || "{}").cues || [];
      } catch (_) { cues = []; }
    }

    const pickCue = (sec) => {
      // 二分查找当前时间所在字幕，避免长字幕频繁遍历。
      let lo = 0;
      let hi = cues.length - 1;
      let best = null;
      while (lo <= hi) {
        const mid = (lo + hi) >> 1;
        const cue = cues[mid];
        if (sec < cue.start) hi = mid - 1;
        else if (sec > cue.end) lo = mid + 1;
        else { best = cue; break; }
      }
      return best;
    };

    const onTime = () => {
      if (!showSubs || !cues.length) {
        subtitleLine.hidden = true;
        return;
      }
      const cue = pickCue(player.currentTime);
      if (cue?.text) {
        subtitleLine.textContent = cue.text.replace(/\n/g, " ");
        subtitleLine.hidden = false;
      } else {
        subtitleLine.hidden = true;
      }
    };
    player.addEventListener("timeupdate", onTime);
    player.addEventListener("error", () => showError(errorBox));

    // 快捷键：空格播放/暂停，左右 ±5s，M 静音，F 全屏。
    const onKey = (ev) => {
      const tag = (ev.target?.tagName || "").toLowerCase();
      if (tag === "input" || tag === "textarea" || tag === "select") return;
      if (ev.code === "Space") {
        ev.preventDefault();
        if (player.paused) player.play().catch(() => {});
        else player.pause();
      } else if (ev.key === "ArrowLeft") {
        ev.preventDefault();
        player.currentTime = Math.max(0, player.currentTime - 5);
      } else if (ev.key === "ArrowRight") {
        ev.preventDefault();
        player.currentTime = Math.min(player.duration || 0, player.currentTime + 5);
      } else if (ev.key.toLowerCase() === "m") {
        player.muted = !player.muted;
        toast(player.muted ? "已静音" : "已取消静音", "", "info", 1200);
      } else if (ev.key.toLowerCase() === "f" && isVideo) {
        if (document.fullscreenElement) document.exitFullscreen?.();
        else player.requestFullscreen?.();
      }
    };
    document.addEventListener("keydown", onKey);
    cleanups.push(() => document.removeEventListener("keydown", onKey));
  };

  const cleanup = () => {
    for (const fn of cleanups) {
      try { fn(); } catch (_) { /* 忽略 */ }
    }
  };

  return { element, mount, cleanup };
}

function kv(key, value) {
  return `<div class="play-kv-row"><span class="k">${escHtml(key)}</span><span class="v" title="${escHtml(String(value))}">${escHtml(String(value))}</span></div>`;
}
