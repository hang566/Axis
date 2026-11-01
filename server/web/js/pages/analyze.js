// 媒体分析：格式概览、流明细、原始 ffprobe JSON。
import { get } from "../api.js";
import { h } from "../dom.js";
import { icon, toast, bytes, duration, escHtml, openModal } from "../ui.js";
import { pickFile } from "../app.js";
import { streamCards, formatCard, emptyState } from "../components/media.js";

export default {
  async render(root, { id }) {
    if (!id) {
      const page = h("div", { class: "stack" });
      page.innerHTML = `
        <div class="page-head">
          <div><h1>媒体分析</h1><p class="sub">查看容器、编码、分辨率、音轨与码率等细节，也可以复制原始 ffprobe 数据。</p></div>
        </div>
      `;
      const zone = h("div", { class: "dropzone" });
      zone.innerHTML = `
        <div class="dz-icon">${icon("search", 24)}</div>
        <strong>选择要分析的媒体文件</strong>
        <p>分析只读取文件信息，不会复制或修改原文件</p>
      `;
      zone.addEventListener("click", () => pickFile({ route: "analyze", temporary: true }));
      page.appendChild(zone);
      root.replaceChildren(page);
      return null;
    }

    let data;
    try {
      data = await get(`/api/projects/${id}/probe`);
    } catch (err) {
      root.replaceChildren(emptyState({
        iconName: "search", title: "无法读取媒体信息", message: err.message,
        action: h("a", { class: "btn btn-sm btn-primary", href: "#/projects" }, "返回项目库"),
      }));
      return null;
    }
    const projectData = await get(`/api/projects/${id}`).catch(() => null);
    const project = projectData?.project;
    const probe = data.probe;

    const page = h("div", { class: "stack" });
    const head = h("div", { class: "page-head" });
    head.innerHTML = `
      <div style="min-width:0">
        <h1 style="font-size:22px">${escHtml(project?.title || data.asset?.fileName || "媒体分析")}</h1>
        <p class="sub">${escHtml(data.asset?.fileName || "")} · ${bytes(data.asset?.size || 0)} · ${duration(probe.format?.duration || project?.duration || 0)}</p>
      </div>
      <span class="spacer"></span>
      <div class="actions">
        <button class="btn btn-sm btn-ghost" data-role="raw">${icon("more", 15)} 原始数据</button>
        <button class="btn btn-sm btn-ghost" data-role="copy">复制 JSON</button>
        ${project ? `<a class="btn btn-sm btn-ghost" href="#/project/${project.id}">项目详情</a>` : ""}
      </div>
    `;
    page.appendChild(head);

    // 预览入口：统一跳到播放页，避免内嵌播放器在文件缺失时显示浏览器默认错误。
    const vStream = (probe.streams || []).find((s) => s.type === "video");
    const preview = h("div", { class: "card card-pad" });
    const thumb = vStream
      ? `<div class="thumb" style="width:132px;height:80px;background-image:url(/api/projects/${id}/thumbnail?at=1)"></div>`
      : `<div class="thumb audio" style="width:132px;height:80px">${icon("audio", 22)}</div>`;
    preview.innerHTML = `
      <div class="media-summary">
        ${thumb}
        <div class="info" style="flex:1">
          <h3 style="font-size:14px">${escHtml(data.asset?.fileName || "")}</h3>
          <div class="facts">
            <span>${duration(probe.format?.duration || project?.duration || 0)}</span>
            <span>${bytes(data.asset?.size || 0)}</span>
            ${vStream?.width ? `<span>${vStream.width}×${vStream.height}</span>` : ""}
          </div>
        </div>
        <a class="btn btn-sm btn-primary" href="#/player/${id}">${icon("play", 14)} 播放</a>
      </div>
    `;
    page.appendChild(preview);

    const grid = h("div", { class: "analyze-grid" });
    grid.append(formatCard(probe, project || { media: data.asset, duration: probe.duration }));
    const metaCard = h("div", { class: "card card-pad" });
    const s = probe.streams || [];
    const v = s.find((x) => x.type === "video");
    const a = s.find((x) => x.type === "audio");
    metaCard.innerHTML = `
      <h4 style="margin:0 0 12px;font-size:12.5px;font-weight:600;color:var(--text-2)">能力判断</h4>
      <div class="stack-sm">
        ${capability("包含视频轨", Boolean(v))}
        ${capability("包含音频轨", Boolean(a))}
        ${capability("可直接在浏览器播放", isBrowserPlayable(data.asset?.fileName || "", v, a), "MP4/M4A/WebM + H.264/AAC/VP9 最常见")}
        ${capability("适合语音识别", Boolean(a), a ? `音频 ${a.codec} ${a.sampleRate}Hz ${a.channels}ch` : "没有音轨，需要先换一个有声音的文件")}
      </div>
    `;
    grid.appendChild(metaCard);
    page.appendChild(grid);

    if (s.length) {
      page.appendChild(h("div", { class: "section-title" }, h("h2", {}, "流明细"), h("span", { class: "spacer" }), h("span", { class: "muted" }, `${s.length} 条`)));
      page.appendChild(streamCards(probe));
    }

    page.querySelector("[data-role=raw]").addEventListener("click", () => {
      openModal({
        title: "原始 ffprobe 数据",
        wide: true,
        content: h("pre", { class: "code", style: "max-height:56vh" }, prettify(data.raw || "{}")),
      });
    });
    page.querySelector("[data-role=copy]").addEventListener("click", async () => {
      try {
        await navigator.clipboard.writeText(prettify(data.raw || "{}"));
        toast("已复制", "ffprobe JSON 已写入剪贴板", "success", 1800);
      } catch (_) {
        toast("复制失败", "浏览器拒绝了剪贴板访问", "warn");
      }
    });

    root.replaceChildren(page);
    return null;
  },
};

function capability(label, ok, hint) {
  return `<div style="display:flex;gap:8px;align-items:flex-start">
    <span class="tag ${ok ? "tag-success" : "tag-warning"}" style="margin-top:1px">${ok ? "是" : "否"}</span>
    <div><div style="font-size:13px">${escHtml(label)}</div>${hint ? `<div class="small muted">${escHtml(hint)}</div>` : ""}</div>
  </div>`;
}

function isBrowserPlayable(name, video, audio) {
  const ext = (name.split(".").pop() || "").toLowerCase();
  const containerOK = ["mp4", "m4a", "webm", "mov", "mp3", "wav", "ogg", "oga", "aac"].includes(ext);
  const codecOK = !video || ["h264", "vp9", "vp8", "av1"].includes((video.codec || "").toLowerCase());
  const audioOK = !audio || ["aac", "mp3", "opus", "vorbis", "flac", "pcm_s16le"].includes((audio.codec || "").toLowerCase());
  return containerOK && codecOK && audioOK;
}

function prettify(raw) {
  try {
    return JSON.stringify(JSON.parse(raw), null, 2);
  } catch (_) {
    return raw;
  }
}
