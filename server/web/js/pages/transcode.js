// 视频转码：预设优先，高级参数可展开，实时命令预览与进度。
import { get, post } from "../api.js";
import { h, debounce } from "../dom.js";
import { icon, toast, bytes, duration, escHtml, progressRing } from "../ui.js";

import { pickFile } from "../app.js";

const PRESET_ICONS = {
  globe: "globe", sparkles: "sparkles", archive: "archive", monitor: "monitor",
  shuffle: "shuffle", audio: "audio", film: "film",
};

export default {
  async render(root, { id }) {
    let project = null;
    let source = { id: "", name: "", duration: 0, size: 0, hasVideo: false };
    if (id) {
      try {
        const data = await get(`/api/projects/${id}`);
        project = data.project;
        source = {
          id: project.id,
          name: project.media?.fileName || "",
          duration: project.duration || 0,
          size: project.media?.size || 0,
          hasVideo: Boolean(project.media?.probe?.hasVideo),
        };
      } catch (err) {
        toast("项目不存在", "请重新选择素材", "error");
      }
    }

    const presetsResp = await get("/api/transcode/presets").catch(() => ({ presets: [] }));
    const presets = presetsResp.presets || [];

    const page = h("div", { class: "stack" });
    const head = h("div", { class: "page-head" });
    head.innerHTML = `
      <div>
        <h1>视频转码</h1>
        <p class="sub">选一个预设就能用。需要精细控制时再展开高级参数，右侧会实时显示将要执行的 ffmpeg 命令。</p>
      </div>
      <span class="spacer"></span>
      <div class="actions">
        ${source.id ? "" : `<button class="btn btn-ghost btn-sm" data-role="pick">${icon("upload", 15)} 选择文件</button>`}
      </div>
    `;
    page.appendChild(head);

    // 源文件卡片
    if (source.id) {
      const src = h("div", { class: "card card-pad" });
      src.innerHTML = `
        <div class="media-summary">
          ${source.hasVideo ? `<div class="thumb" style="width:120px;height:74px;background-image:url(/api/projects/${source.id}/thumbnail?at=1)"></div>` : `<div class="thumb audio" style="width:120px;height:74px">${icon("audio", 22)}</div>`}
          <div class="info">
            <h3 style="font-size:15px">${escHtml(source.name)}</h3>
            <div class="facts"><span>${duration(source.duration)}</span><span>${bytes(source.size)}</span></div>
          </div>
          <span class="spacer"></span>
          <a class="btn btn-sm btn-ghost" href="#/project/${source.id}">项目详情</a>
        </div>
      `;
      page.appendChild(src);
    } else {
      const zone = h("div", { class: "dropzone" });
      zone.innerHTML = `
        <div class="dz-icon">${icon("shuffle", 24)}</div>
        <strong>选择要转码的音视频</strong>
        <p>也可以先把文件拖到窗口任意位置，再从项目里进入转码</p>
      `;
      zone.addEventListener("click", () => pickFile({ route: "transcode", temporary: true, onImported: () => location.reload() }));
      page.appendChild(zone);
      root.replaceChildren(page);
      return null;
    }

    // 预设
    const presetCard = h("div", { class: "card card-pad" });
    presetCard.innerHTML = `<h4 style="margin:0 0 12px;font-size:12.5px;font-weight:600;color:var(--text-2)">输出预设</h4>`;
    const grid = h("div", { class: "preset-grid" });
    let selected = presets[0]?.id || "web-mp4";
    const cards = [];
    presets.forEach((preset) => {
      const card = h("button", { class: "preset-card" + (preset.id === selected ? " active" : ""), type: "button" });
      card.innerHTML = `
        <span class="pi">${icon(PRESET_ICONS[preset.icon] || "film", 16)}</span>
        <span><strong>${escHtml(preset.name)}</strong><span>${escHtml(preset.description)}</span></span>
      `;
      card.addEventListener("click", () => {
        selected = preset.id;
        cards.forEach((c) => c.classList.toggle("active", c === card));
        applyPreset(preset);
        updatePreview();
      });
      cards.push(card);
      grid.appendChild(card);
    });
    presetCard.appendChild(grid);
    page.appendChild(presetCard);

    // 高级参数
    const advCard = h("div", { class: "card" });
    advCard.innerHTML = `
      <div class="card-head">
        <h3>高级参数</h3>
        <span class="spacer"></span>
        <button class="btn btn-xs btn-ghost" type="button" data-role="toggle">展开</button>
      </div>
      <div class="card-body hidden" data-role="body" style="padding-top:14px"></div>
    `;
    const body = advCard.querySelector("[data-role=body]");
    const toggle = advCard.querySelector("[data-role=toggle]");
    toggle.addEventListener("click", () => {
      body.classList.toggle("hidden");
      toggle.textContent = body.classList.contains("hidden") ? "展开" : "收起";
    });
    page.appendChild(advCard);

    const fields = buildAdvancedFields(source);
    body.appendChild(fields.element);

    // 底部：命令预览 + 开始
    const actionCard = h("div", { class: "card card-pad" });
    actionCard.innerHTML = `
      <div class="cluster">
        <button class="btn btn-primary btn-lg" type="button" data-role="start">${icon("shuffle", 18)} 开始转码</button>
        <span class="spacer"></span>
        <span class="small muted" data-role="outname"></span>
      </div>
      <pre class="code" data-role="command" style="margin-top:14px">生成命令预览…</pre>
      <div data-role="progress" style="margin-top:12px"></div>
    `;
    page.appendChild(actionCard);

    const startBtn = actionCard.querySelector("[data-role=start]");
    const commandBox = actionCard.querySelector("[data-role=command]");
    const outName = actionCard.querySelector("[data-role=outname]");
    const progressHost = actionCard.querySelector("[data-role=progress]");

    const currentParams = () => {
      const params = { ...fields.values(), preset: selected };
      return params;
    };

    const updatePreview = debounce(async () => {
      if (!project) return;
      try {
        const res = await post("/api/transcode/preview", { projectId: project.id, params: currentParams() });
        commandBox.textContent = res.command || "";
        outName.textContent = "输出：" + (res.fileName || "");
      } catch (err) {
        commandBox.textContent = "预览失败：" + err.message;
      }
    }, 260);

    function applyPreset(preset) {
      const p = preset || presets.find((x) => x.id === selected);
      if (!p) return;
      fields.set({
        format: p.format,
        videoCodec: p.videoCodec,
        audioCodec: p.audioCodec,
        speed: p.speed,
        crf: p.crf,
        bitrate: p.bitrate,
        audioBitrate: p.audioBitrate,
        resolution: p.resolution,
        frameRate: p.frameRate,
      });
    }
    applyPreset();

    fields.element.addEventListener("input", updatePreview);
    fields.element.addEventListener("change", updatePreview);
    updatePreview();

    let jobId = "";
    let pollTimer = null;

    const start = async () => {
      if (jobId) return;
      startBtn.disabled = true;
      progressHost.replaceChildren(h("div", { class: "progress striped" }, h("i", { style: "width:5%" })));
      try {
        const res = await post("/api/jobs", {
          type: "transcode",
          projectId: project.id,
          params: currentParams(),
        });
        jobId = res.jobs?.[0]?.id || "";
        toast("转码已开始", "可以离开此页，任务会在后台继续", "info");
        poll();
      } catch (err) {
        startBtn.disabled = false;
        progressHost.replaceChildren();
        toast("转码启动失败", err.message, "error");
        if (err.hint) setTimeout(() => toast("处理建议", err.hint, "info", 7000), 300);
      }
    };
    startBtn.addEventListener("click", start);

    const poll = async () => {
      if (!jobId) return;
      clearTimeout(pollTimer);
      try {
        const data = await get(`/api/jobs/${jobId}`);
        const job = data.job;
        if (job.status === "running" || job.status === "pending") {
          renderRunning(progressHost, job);
          pollTimer = setTimeout(poll, 900);
        } else if (job.status === "succeeded") {
          renderDone(progressHost, job, project);
          startBtn.disabled = false;
          jobId = "";
          toast("转码完成", job.result?.message || "", "success");
        } else {
          renderFailed(progressHost, job, start);
          startBtn.disabled = false;
          jobId = "";
        }
      } catch (err) {
        pollTimer = setTimeout(poll, 2000);
      }
    };

    page.querySelector("[data-role=pick]")?.addEventListener("click", () => pickFile({ route: "transcode", temporary: true }));

    root.replaceChildren(page);
    document.addEventListener("jobs:update", onJobUpdate);
    function onJobUpdate(ev) {
      const list = ev.detail?.jobs || [];
      const job = list.find((j) => j.id === jobId);
      if (job) renderRunning(progressHost, job);
    }
    return () => {
      clearTimeout(pollTimer);
      document.removeEventListener("jobs:update", onJobUpdate);
    };
  },
};

function buildAdvancedFields(source) {
  const values = {
    format: "mp4", videoCodec: "libx264", audioCodec: "aac", speed: "medium",
    crf: 23, bitrate: "", audioBitrate: "160k", resolution: "", frameRate: 0,
    sampleRate: 0, channels: 0, start: 0, end: 0,
  };
  const wrapper = h("div", { class: "stack" });

  const grid1 = h("div", { class: "row" });
  grid1.append(
    field("容器格式", selectEl("format", ["mp4", "mkv", "webm", "mov", "m4a", "mp3"], values.format)),
    field("视频编码", selectEl("videoCodec", [
      ["libx264", "H.264（兼容性最好）"], ["libx265", "H.265（体积更小）"],
      ["libvpx-vp9", "VP9（WebM）"], ["copy", "不重编码（最快）"], ["none", "不要视频"],
    ], values.videoCodec)),
    field("音频编码", selectEl("audioCodec", [
      ["aac", "AAC"], ["libmp3lame", "MP3"], ["libopus", "Opus"],
      ["copy", "不重编码"], ["none", "不要音频"],
    ], values.audioCodec)),
  );
  wrapper.appendChild(grid1);

  const grid2 = h("div", { class: "row" });
  grid2.append(
    field("编码速度", selectEl("speed", [
      ["ultrafast", "ultrafast（最快）"], ["veryfast", "veryfast"], ["fast", "fast"],
      ["medium", "medium（均衡）"], ["slow", "slow（更小）"],
    ], values.speed)),
    numberField("质量 CRF", "crf", values.crf, { min: 0, max: 51 }, "数值越小画质越好；23 是常用值"),
    textField("分辨率", "resolution", values.resolution, "720 或 1280x720"),
    textField("视频码率", "bitrate", values.bitrate, "如 2M，留空用 CRF"),
  );
  wrapper.appendChild(grid2);

  const grid3 = h("div", { class: "row" });
  grid3.append(
    textField("音频码率", "audioBitrate", values.audioBitrate, "如 128k"),
    numberField("帧率", "frameRate", values.frameRate, { min: 0, max: 120, step: 1 }, "留空保持原帧率"),
    numberField("起点（秒）", "start", values.start, { min: 0, step: 0.1 }, "剪辑起点"),
    numberField("终点（秒）", "end", values.end, { min: 0, step: 0.1 }, `0 表示到结尾（共 ${Math.round(source.duration)} 秒）`),
  );
  wrapper.appendChild(grid3);

  const note = h("div", { class: "field-hint" }, "提示：选择「不重编码」时几秒即可完成，但只有容器兼容时才可用；分辨率留空表示保持原尺寸。");
  wrapper.appendChild(note);

  const inputs = () => wrapper.querySelectorAll("[data-key]");
  wrapper.addEventListener("input", (ev) => {
    const key = ev.target.dataset.key;
    if (!key) return;
    values[key] = numeric(key) ? Number(ev.target.value) : ev.target.value;
  });

  return {
    element: wrapper,
    values: () => ({ ...values }),
    set(partial) {
      Object.keys(partial).forEach((key) => {
        if (partial[key] === undefined || partial[key] === null) return;
        values[key] = partial[key];
      });
      inputs().forEach((input) => {
        const key = input.dataset.key;
        if (key in values) input.value = values[key];
      });
    },
  };
}

function numeric(key) {
  return ["crf", "frameRate", "start", "end", "sampleRate", "channels"].includes(key);
}

function field(label, control, hint) {
  const el = h("div", { class: "field" });
  el.appendChild(h("label", {}, label));
  el.appendChild(control);
  if (hint) el.appendChild(h("span", { class: "field-hint" }, hint));
  return el;
}

function selectEl(key, options, value) {
  const sel = h("select", { class: "select", "data-key": key });
  options.forEach((opt) => {
    const [val, label] = Array.isArray(opt) ? opt : [opt, opt];
    sel.appendChild(h("option", { value: val, selected: val === value }, label));
  });
  sel.value = value;
  return sel;
}

function numberField(label, key, value, attrs = {}, hint) {
  const input = h("input", { class: "input", type: "number", "data-key": key, value: value || "", ...attrs });
  return field(label, input, hint);
}

function textField(label, key, value, placeholder) {
  const input = h("input", { class: "input", type: "text", "data-key": key, value: value || "", placeholder: placeholder || "" });
  return field(label, input);
}

function renderRunning(host, job) {
  const pct = Math.round((job.overall || 0) * 100);
  host.innerHTML = `
    <div class="cluster" style="gap:12px">
      <div class="run-ring" style="width:42px;height:42px">${progressRing(pct / 100, 42, 3)}<span class="pct" style="font-size:10px">${pct}%</span></div>
      <div style="flex:1;min-width:0">
        <div style="font-size:13px;font-weight:600">${escHtml(job.phase || "转码中")}</div>
        <div class="progress thin striped" style="margin-top:6px"><i style="width:${pct}%"></i></div>
      </div>
      <button class="btn btn-xs btn-danger-soft" type="button" data-role="cancel">取消</button>
    </div>
  `;
  host.querySelector("[data-role=cancel]")?.addEventListener("click", async () => {
    try { await post(`/api/jobs/${job.id}/cancel`); toast("已请求取消", "", "warn"); } catch (err) { toast("取消失败", err.message, "error"); }
  });
}

function renderDone(host, job, project) {
  const res = job.result || {};
  host.innerHTML = `
    <div class="alert alert-success">
      <span class="ic">${icon("check", 18)}</span>
      <div><strong>转码完成</strong>${escHtml(res.message || "")}</div>
    </div>
    <div class="cluster" style="margin-top:12px">
      ${res.outputId ? `<a class="btn btn-sm btn-primary" href="/api/assets/${res.outputId}/download">下载成片</a>
      <a class="btn btn-sm btn-ghost" href="/api/assets/${res.outputId}/media" target="_blank" rel="noopener">播放</a>` : ""}
      <a class="btn btn-sm btn-ghost" href="#/project/${project.id}">回到项目</a>
    </div>
  `;
}

function renderFailed(host, job, retry) {
  host.innerHTML = `
    <div class="alert alert-danger">
      <span class="ic">${icon("alert", 18)}</span>
      <div><strong>转码失败</strong>${escHtml(job.error || "")}${job.errorHint ? `<div class="small" style="margin-top:4px;opacity:.85">${escHtml(job.errorHint)}</div>` : ""}</div>
    </div>
  `;
  const btn = h("button", { class: "btn btn-sm btn-soft", type: "button", style: "margin-top:10px" }, "重试");
  btn.addEventListener("click", retry);
  host.appendChild(btn);
}



