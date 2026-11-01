// 共享展示组件：媒体摘要、流信息、状态徽标、空状态。
import { h } from "../dom.js";
import { icon, bytes, duration, resolution, escHtml, timecode, relTime, dateTime } from "../ui.js";

export function statusTag(status) {
  const map = {
    ready: ["tag-success", "已完成"],
    processing: ["tag-accent", "处理中"],
    failed: ["tag-danger", "失败"],
    draft: ["tag", "草稿"],
  };
  const [cls, label] = map[status] || map.draft;
  return h("span", { class: "tag " + cls }, label);
}

export function jobStatusTag(status) {
  const map = {
    running: ["tag-accent", "运行中"],
    pending: ["tag", "排队中"],
    succeeded: ["tag-success", "已完成"],
    failed: ["tag-danger", "失败"],
    canceled: ["tag", "已取消"],
    interrupted: ["tag-warning", "已中断"],
  };
  const [cls, label] = map[status] || ["tag", status];
  return h("span", { class: "tag " + cls }, label);
}

export function emptyState({ iconName = "film", title, message, action }) {
  const el = h("div", { class: "empty" });
  el.innerHTML = `${icon(iconName, 44)}<strong>${escHtml(title)}</strong>${message ? `<p>${escHtml(message)}</p>` : ""}`;
  if (action) el.appendChild(action);
  return el;
}

/** 媒体信息摘要（封面 + 关键参数） */
export function mediaCard(project, { linked = false } = {}) {
  const probe = project.media?.probe || {};
  const thumb = probe.hasVideo
    ? `<div class="thumb" style="width:120px;height:74px;background-image:url(/api/projects/${project.id}/thumbnail?at=1)"></div>`
    : `<div class="thumb audio" style="width:120px;height:74px">${icon("audio", 22)}</div>`;
  const card = h("div", { class: "card card-pad" });
  card.innerHTML = `
    <div class="media-summary">
      ${thumb}
      <div class="info" style="flex:1">
        <h3 style="font-size:15px">${escHtml(project.media?.fileName || "媒体文件")}</h3>
        <div class="facts">
          <span>${duration(project.duration)}</span>
          <span>${bytes(project.media?.size || 0)}</span>
          ${probe.hasVideo ? `<span>${resolution(probe)}</span>` : ""}
          ${probe.videoCodec ? `<span>${escHtml(probe.videoCodec)}</span>` : ""}
          ${probe.audioCodec ? `<span>${escHtml(probe.audioCodec)} ${probe.sampleRate ? probe.sampleRate / 1000 + "kHz" : ""}</span>` : ""}
        </div>
      </div>
    </div>
    <div class="cluster" style="margin-top:14px">
      <a class="btn btn-xs btn-primary" href="#/player/${project.id}">${icon("play", 13)} 播放</a>
      <a class="btn btn-xs btn-ghost" href="/api/assets/${project.media?.id}/download">下载原文件</a>
      ${linked ? `<a class="btn btn-xs btn-ghost" href="#/analyze/${project.id}">完整分析</a>` : ""}
    </div>
  `;
  return card;
}

/** 流信息卡片（供分析页与详情页复用） */
export function streamCards(probe) {
  const wrap = h("div", { class: "analyze-grid" });
  const streams = probe?.streams || [];
  if (!streams.length) {
    wrap.appendChild(emptyState({ title: "没有可展示的流", message: "媒体可能只包含容器信息。" }));
    return wrap;
  }
  streams.forEach((s) => wrap.appendChild(streamCard(s)));
  return wrap;
}

function streamCard(stream) {
  const isVideo = stream.type === "video";
  const isAudio = stream.type === "audio";
  const card = h("div", { class: "stream-card" });
  const head = h("div", { class: "head" });
  head.innerHTML = `
    <span class="tag ${isVideo ? "tag-accent" : isAudio ? "tag-success" : ""}">${isVideo ? "视频" : isAudio ? "音频" : escHtml(stream.type)}</span>
    <span class="title">#${stream.index} ${escHtml(stream.codec || "")}</span>
  `;
  card.appendChild(head);

  const rows = [];
  rows.push(["编码", stream.codecLongName || stream.codec || "-"]);
  if (isVideo) {
    rows.push(["分辨率", stream.width ? `${stream.width} × ${stream.height}` : "-"]);
    rows.push(["帧率", stream.frameRate ? stream.frameRate.toFixed(2) + " fps" : "-"]);
    rows.push(["像素格式", stream.pixelFormat || "-"]);
    rows.push(["码率", stream.bitRate ? Math.round(stream.bitRate / 1000) + " kbps" : "-"]);
    rows.push(["时长", duration(stream.duration)]);
  } else if (isAudio) {
    rows.push(["采样率", stream.sampleRate ? stream.sampleRate + " Hz" : "-"]);
    rows.push(["声道", stream.channels ? `${stream.channels}（${stream.channelLayout || "未知"}）` : "-"]);
    rows.push(["采样格式", stream.sampleFormat || "-"]);
    rows.push(["码率", stream.bitRate ? Math.round(stream.bitRate / 1000) + " kbps" : "-"]);
    rows.push(["语言", stream.language || "-"]);
  } else {
    rows.push(["语言", stream.language || "-"]);
    rows.push(["标题", stream.title || "-"]);
  }
  const grid = h("div", { class: "stream-data" });
  grid.innerHTML = rows.map(([k, v]) => `<div class="item"><span class="k">${escHtml(k)}</span><span class="v">${escHtml(v)}</span></div>`).join("");
  card.appendChild(grid);
  return card;
}

/** 容器概要 */
export function formatCard(probe, project) {
  const f = probe?.format || {};
  const card = h("div", { class: "card card-pad" });
  const rows = [
    ["容器", `${f.formatName || "-"}${f.longName ? `（${f.longName}）` : ""}`],
    ["时长", duration(probe?.format?.duration || project?.duration)],
    ["文件大小", bytes(f.size || project?.media?.size || 0)],
    ["总码率", f.bitRate ? Math.round(f.bitRate / 1000) + " kbps" : "-"],
    ["流数量", String((probe?.streams || []).length)],
    ["文件名", project?.media?.fileName || "-"],
  ];
  card.innerHTML = `
    <h4 style="margin:0 0 12px;font-size:12.5px;font-weight:600;color:var(--text-2)">容器信息</h4>
    <div class="stream-data">
      ${rows.map(([k, v]) => `<div class="item"><span class="k">${escHtml(k)}</span><span class="v">${escHtml(v)}</span></div>`).join("")}
    </div>
  `;
  return card;
}

export function subtitleRow(projectId, track, onDeleted) {
  const item = h("div", { class: "list-item" });
  item.innerHTML = `
    <span class="thumb audio" style="width:38px;height:38px;border-radius:8px">${icon("wave", 16)}</span>
    <div class="grow">
      <div class="title">${escHtml(track.fileName || "字幕")}</div>
      <div class="meta">
        <span>${track.cueCount} 条</span>
        <span>${String(track.format || "srt").toUpperCase()}</span>
        <span>${track.edited ? "手工编辑" : escHtml(track.provider || "自动生成")}</span>
        <span>${relTime(track.updatedAt)}</span>
      </div>
    </div>
    <div class="cluster" style="gap:4px"></div>
  `;
  const actions = item.querySelector(".cluster");
  actions.append(
    h("a", { class: "btn btn-xs btn-soft", href: `#/editor/${projectId}?track=${track.id}` }, "编辑"),
    h("a", { class: "btn btn-xs btn-ghost", href: `/api/projects/${projectId}/exports?format=srt&track=${track.id}` }, "SRT"),
    h("a", { class: "btn btn-xs btn-ghost", href: `/api/projects/${projectId}/exports?format=vtt&track=${track.id}` }, "VTT"),
  );
  return item;
}

export function jobRow(job) {
  const item = h("div", { class: "list-item" });
  const pct = Math.round((job.overall || 0) * 100);
  item.innerHTML = `
    <div class="grow">
      <div class="title">${escHtml(job.title || job.type)}</div>
      <div class="meta">
        <span>${escHtml(job.phase || "")}</span>
        <span>${relTime(job.createdAt)}</span>
        ${job.result?.cueCount ? `<span>${job.result.cueCount} 条字幕</span>` : ""}
      </div>
    </div>
  `;
  const tag = jobStatusTag(job.status);
  item.appendChild(tag);
  if (job.status === "running" || job.status === "pending") {
    const bar = h("div", { class: "progress thin striped", style: "width:90px" });
    bar.innerHTML = `<i style="width:${pct}%"></i>`;
    item.appendChild(bar);
  }
  return item;
}

export { bytes, duration, resolution, timecode, dateTime, relTime, escHtml };
