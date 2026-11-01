// 总览：导入入口、资料库概览、最近项目、环境自检。

import { h } from "../dom.js";
import {
  icon, bytes, duration, relTime, resolution, escHtml, openModal,
} from "../ui.js";
import { state, refreshStats, refreshProjects } from "../store.js";
import { pickFile, importFile } from "../app.js";

export default {
  async render(root) {
    const [statsLoaded, projectsLoaded] = await Promise.all([
      refreshStats(),
      refreshProjects(),
    ]);
    root.replaceChildren(buildPage());
    return null;
  },
};

function buildPage() {
  const frag = document.createDocumentFragment();
  const env = state.env || {};
  const stats = state.stats?.library || {};
  const projects = state.projects.slice(0, 6);

  const wrap = h("div", { class: "stack" });

  // ---- Hero ----
  const hero = h("section", { class: "hero" });
  const heroMain = h("div", { class: "hero-main" });
  heroMain.innerHTML = `
    <h1>把音视频变成可编辑的字幕</h1>
    <p>拖入文件即可开始。所有素材、字幕与任务都保存在本机，关掉浏览器也不会丢。</p>
  `;

  const dropzone = h("div", { class: "dropzone", role: "button", tabindex: "0", "aria-label": "选择或拖入媒体文件" });
  dropzone.innerHTML = `
    <div class="dz-icon">${icon("upload", 26)}</div>
    <strong>拖入音视频，或点击选择文件</strong>
    <p>支持 MP4 / MKV / MOV / MP3 / WAV / M4A / FLAC 等常见格式</p>
    <div class="dz-formats">
      <span class="tag tag-mono">MP4</span><span class="tag tag-mono">MKV</span>
      <span class="tag tag-mono">MOV</span><span class="tag tag-mono">MP3</span>
      <span class="tag tag-mono">WAV</span><span class="tag tag-mono">M4A</span>
    </div>
    <div class="upload-bar">
      <div class="meta"><span class="name">正在导入</span><span class="pct">0%</span></div>
      <div class="progress striped"><i style="width:0%"></i></div>
    </div>
  `;
  const openPicker = () => pickFile({ route: "transcribe", temporary: false });
  dropzone.addEventListener("click", openPicker);
  dropzone.addEventListener("keydown", (ev) => {
    if (ev.key === "Enter" || ev.key === " ") { ev.preventDefault(); openPicker(); }
  });
  dropzone.addEventListener("dragover", (ev) => { ev.preventDefault(); dropzone.classList.add("dragover"); });
  dropzone.addEventListener("dragleave", () => dropzone.classList.remove("dragover"));
  dropzone.addEventListener("drop", async (ev) => {
    ev.preventDefault();
    ev.stopPropagation();
    dropzone.classList.remove("dragover");
    const file = ev.dataTransfer?.files?.[0];
    if (!file) return;
    const bar = dropzone.querySelector(".upload-bar");
    bar.classList.add("show");
    const payload = await importFile(file, { temporary: false });
    if (payload?.project?.id) location.hash = `#/transcribe/${payload.project.id}`;
    else bar.classList.remove("show");
  });
  heroMain.appendChild(dropzone);

  const quick = h("div", { class: "quick-grid", style: "margin-top:16px" });
  quick.append(
    quickCard("film", "字幕转写", "识别语音并生成 SRT / VTT", () => pickFile({ route: "transcribe", temporary: false })),
    quickCard("shuffle", "视频转码", "改格式、压体积、提取音频", () => pickFile({ route: "transcode", temporary: true })),
    quickCard("search", "媒体分析", "查看编码、分辨率、音轨细节", () => pickFile({ route: "analyze", temporary: true })),
  );
  heroMain.appendChild(quick);

  const aside = h("div", { class: "stack" });
  aside.appendChild(healthCard(env));
  hero.append(heroMain, aside);
  wrap.appendChild(hero);

  // ---- 统计 ----
  const statGrid = h("div", { class: "stat-grid", style: "margin-top:18px" });
  statGrid.append(
    statCard("项目总数", String(stats.projects ?? 0), "个"),
    statCard("字幕已完成", String(stats.readyCount ?? 0), "个"),
    statCard("占用空间", bytes(stats.mediaBytes ?? 0).split(" ")[0], bytes(stats.mediaBytes ?? 0).split(" ")[1] || ""),
    statCard("素材数量", String(stats.assets ?? 0), "份"),
  );
  wrap.appendChild(statGrid);

  // ---- 最近项目 ----
  if (projects.length) {
    const section = h("div", { class: "section-title" },
      h("h2", {}, "最近打开"),
      h("span", { class: "spacer" }),
      h("a", { href: "#/projects", class: "small" }, "查看全部 →"),
    );
    wrap.appendChild(section);
    const grid = h("div", { class: "project-grid" });
    projects.forEach((p) => grid.appendChild(miniProject(p)));
    wrap.appendChild(grid);
  }

  // ---- 最近任务 ----
  const jobs = (state.stats?.recentJobs || []).filter((j) => j.status !== "succeeded").slice(0, 4);
  const active = (state.stats?.recentJobs || []).filter((j) => j.status === "running" || j.status === "pending");
  if (active.length) {
    wrap.appendChild(h("div", { class: "section-title" }, h("h2", {}, "正在处理"),
      h("span", { class: "spacer" }),
      h("a", { href: "#/projects", class: "small" }, "任务中心 →")));
    const card = h("div", { class: "card card-pad" });
    const list = h("div", { class: "list" });
    active.forEach((job) => {
      const pct = Math.round((job.overall || 0) * 100);
      const item = h("div", { class: "list-item" });
      item.innerHTML = `
        <div class="grow">
          <div class="title">${escHtml(job.title || job.type)}</div>
          <div class="meta"><span>${escHtml(job.phase || "")}</span></div>
        </div>
        <div style="width:120px"><div class="progress thin striped"><i style="width:${pct}%"></i></div></div>
        <span class="small muted nowrap">${pct}%</span>
      `;
      list.appendChild(item);
    });
    card.appendChild(list);
    wrap.appendChild(card);
  }

  frag.appendChild(wrap);
  return frag;
}

function quickCard(ic, title, desc, onClick) {
  const card = h("button", { class: "quick-card", type: "button" });
  card.innerHTML = `
    <span class="qi">${icon(ic, 18)}</span>
    <span class="qtext"><strong>${escHtml(title)}</strong><span>${escHtml(desc)}</span></span>
  `;
  card.addEventListener("click", onClick);
  return card;
}

function statCard(label, value, unit) {
  const el = h("div", { class: "stat" });
  el.innerHTML = `
    <span class="k">${escHtml(label)}</span>
    <span class="v">${escHtml(value)}${unit ? `<small>${escHtml(unit)}</small>` : ""}</span>
  `;
  return el;
}

function healthCard(env) {
  const card = h("div", { class: "card card-pad" });
  const rows = [
    { name: "ffmpeg", ok: env.ffmpeg?.ok, detail: env.ffmpeg?.ok ? "可用" : "缺失", fix: env.ffmpeg?.fix },
    { name: "ffprobe", ok: env.ffprobe?.ok, detail: env.ffprobe?.ok ? "可用" : "缺失", fix: env.ffprobe?.fix },
    { name: "Python / sherpa", ok: env.python?.ok, detail: env.python?.ok ? (env.python.version || "可用") : "未就绪", fix: env.python?.fix },
    { name: "语音模型", ok: (env.installedModels || []).length > 0, detail: `${(env.installedModels || []).length} 个`, fix: "打开「模型中心」下载一个中文模型（推荐 SenseVoice）" },
  ];
  card.innerHTML = `
    <div style="display:flex;align-items:center;gap:8px;margin-bottom:4px">
      <strong style="font-size:13.5px">运行环境</strong>
      <span class="spacer"></span>
      <span class="tag ${env.ready ? "tag-success" : "tag-warning"}">${env.ready ? "就绪" : "待完善"}</span>
    </div>
    <div class="health-list"></div>
  `;
  const list = card.querySelector(".health-list");
  rows.forEach((row) => {
    const item = h("div", { class: "health-item" });
    item.innerHTML = `
      <span class="name">${row.name}</span>
      <span class="status tag ${row.ok ? "tag-success" : "tag-warning"}">${escHtml(row.detail)}</span>
    `;
    if (!row.ok && row.fix) {
      item.style.cursor = "pointer";
      item.title = row.fix;
      item.addEventListener("click", () => openModal({
        title: row.name + " 未就绪",
        content: h("p", {}, row.fix),
        footer: h("button", { class: "btn btn-primary", type: "button", onclick: () => {
          document.getElementById("modal-root").hidden = true;
          location.hash = "#/settings";
        }}, "去设置"),
      }));
    }
    list.appendChild(item);
  });
  const disk = env.disk || {};
  const info = h("div", { class: "small muted", style: "margin-top:10px" });
  info.textContent = `数据目录：${state.system?.dataDir || ""} · 剩余 ${bytes(disk.freeBytes || 0)}`;
  card.appendChild(info);
  return card;
}

function miniProject(p) {
  const card = h("article", { class: "project-card" });
  const cover = p.media?.probe?.hasVideo
    ? `background-image:url(/api/projects/${p.id}/thumbnail?at=1)`
    : "";
  card.innerHTML = `
    <div class="project-cover" style="${cover}">
      ${p.media?.probe?.hasVideo ? "" : icon(p.media?.probe?.hasAudio ? "audio" : "film", 28)}
      <span class="cover-badge tag ${statusTag(p.status).cls}">${statusTag(p.status).label}</span>
    </div>
    <div class="project-body">
      <h3>${escHtml(p.title)}</h3>
      <div class="meta">
        <span>${duration(p.duration)}</span>
        ${p.media?.probe?.width ? `<span>${resolution(p.media.probe)}</span>` : ""}
        <span>${relTime(p.updatedAt)}</span>
      </div>
      <div class="foot">
        <span class="tag">${p.subtitles?.length || 0} 份字幕</span>
        <span class="spacer"></span>
        <span class="small muted">打开 →</span>
      </div>
    </div>
  `;
  card.addEventListener("click", () => { location.hash = `#/project/${p.id}`; });
  return card;
}

function statusTag(status) {
  return {
    ready: { cls: "tag-success", label: "已完成" },
    processing: { cls: "tag-accent", label: "处理中" },
    failed: { cls: "tag-danger", label: "失败" },
    draft: { cls: "tag", label: "草稿" },
  }[status] || { cls: "tag", label: "草稿" };
}

