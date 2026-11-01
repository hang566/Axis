// 转写页：导入/选择媒体 → 配置 → 启动任务 → 实时进度 → 结果。
import { get, post } from "../api.js";
import { h } from "../dom.js";
import {
  icon, toast, bytes, duration, resolution, escHtml, progressRing,
} from "../ui.js";
import { state } from "../store.js";
import { pickFile } from "../app.js";

const PRESETS = {
  balanced: {
    label: "均衡", hint: "适合口播、访谈；一句话一行",
    sub: { maxLineLength: 42, maxLines: 2, maxCue: 6, minCue: 700, maxPause: 1500, sentence: true, strip: true },
  },
  compact: {
    label: "短句", hint: "句子更短，适合短视频；阅读压力小",
    sub: { maxLineLength: 24, maxLines: 2, maxCue: 4, minCue: 600, maxPause: 900, sentence: true, strip: true },
  },
  full: {
    label: "完整标点", hint: "保留标点，适合存档、二次校对",
    sub: { maxLineLength: 46, maxLines: 2, maxCue: 7, minCue: 800, maxPause: 1800, sentence: true, strip: false },
  },
  verbatim: {
    label: "逐句", hint: "不合并句子，最贴近识别原始分段",
    sub: { maxLineLength: 50, maxLines: 3, maxCue: 10, minCue: 300, maxPause: 3000, sentence: false, strip: false },
  },
};

const LANGS = [
  ["", "自动检测"], ["zh", "中文"], ["en", "English"], ["ja", "日本語"],
  ["ko", "한국어"], ["yue", "粤语"],
];

export default {
  async render(root, { id }) {
    let disposed = false;
    let project = null;
    let probe = null;
    let unlisten = null;

    if (id) {
      try {
        const data = await get(`/api/projects/${id}`);
        project = data.project;
        probe = project.media?.probe || null;
      } catch (err) {
        toast("项目不存在", "可能已被删除，请从项目库重新选择", "error");
        location.hash = "#/transcribe";
        return null;
      }
    }

    const page = buildShell(project, probe);
    root.replaceChildren(page.element);

    if (project) {
      // 已有字幕时，默认“覆盖最近一份”，避免每次生成一堆版本。
      if (project.subtitles?.length) {
        page.state.subtitleId = project.subtitles[0].id;
      }
      // 自动带入上次使用的语言。
      if (project.language && !page.state.language) {
        page.state.language = project.language;
        page.languageSelect.value = project.language;
      }
    }

    const listen = (ev) => {
      const payload = ev.detail;
      if (!payload || disposed) return;
      const job = findProjectJob(payload.jobs, project?.id);
      if (!job) return;
      page.updateJob(job);
      if (job.status === "succeeded" && !page.finished) {
        page.finished = true;
        handleSuccess(job, page, project?.id);
      }
    };
    document.addEventListener("jobs:update", listen);
    unlisten = () => document.removeEventListener("jobs:update", listen);

    const timer = setInterval(async () => {
      if (disposed || !page.currentJob) return;
      try {
        const data = await get(`/api/jobs/${page.currentJob}/logs`);
        page.appendLogs(data.logs || []);
        const jobData = await get(`/api/jobs/${page.currentJob}`);
        page.updateJob(jobData.job);
        if (!["running", "pending"].includes(jobData.job.status) && !page.finished) {
          page.finished = true;
          if (jobData.job.status === "succeeded") handleSuccess(jobData.job, page, project?.id);
          else page.showError(jobData.job);
        }
      } catch (_) { /* 任务可能已被清理 */ }
    }, 1500);

    return () => {
      disposed = true;
      clearInterval(timer);
      unlisten?.();
    };
  },
};

function findProjectJob(jobs, projectId) {
  if (!Array.isArray(jobs)) return null;
  return jobs.find((j) => j.projectId === projectId && j.type === "transcribe") || null;
}

function buildShell(project, probe) {
  const s = state.settings || {};
  const modelList = state.env?.installedModels || [];
  const engineReady = Boolean(state.env?.ready);

  const stateObj = {
    projectId: project?.id || "",
    language: s.language || "",
    format: s.outputFormat || "srt",
    preset: "balanced",
    modelId: s.modelId || "",
    subtitleId: "",
    currentJob: "",
    finished: false,
    temp: !project, // 没有项目时需要先导入
  };

  const element = h("div", {});

  // 如果没有项目，先给上传入口。
  if (!project) {
    element.appendChild(renderImportPrompt());
    return { element, state: stateObj, updateJob() {}, appendLogs() {}, showError() {}, languageSelect: h("select") };
  }

  const page = h("div", { class: "transcribe-layout" });

  // ---------- 左侧 ----------
  const main = h("div", { class: "transcribe-main" });

  const head = h("div", { class: "page-head", style: "margin-bottom:0" });
  head.innerHTML = `
    <div style="min-width:0">
      <h1 style="font-size:22px">${escHtml(project.title)}</h1>
      <p class="sub">选择识别参数后开始。任务在后台运行，可以离开此页，进度不会丢。</p>
    </div>
    <span class="spacer"></span>
    <div class="actions">
      <a class="btn btn-ghost btn-sm" href="#/project/${project.id}">项目详情</a>
    </div>
  `;
  main.appendChild(head);

  // 媒体摘要
  const mediaCard = h("div", { class: "card card-pad" });
  const thumb = project.media?.probe?.hasVideo
    ? `<div class="thumb" style="background-image:url(/api/projects/${project.id}/thumbnail?at=1)"></div>`
    : `<div class="thumb audio" style="width:96px;height:60px">${icon("audio", 22)}</div>`;
  const p = probe || {};
  mediaCard.innerHTML = `
    <div class="media-summary">
      ${thumb}
      <div class="info">
        <h3>${escHtml(project.media?.fileName || "媒体文件")}</h3>
        <div class="facts">
          <span>${duration(project.duration)}</span>
          <span>${bytes(project.media?.size || 0)}</span>
          ${p.hasVideo ? `<span>${resolution(p)}</span>` : ""}
          ${p.videoCodec ? `<span>${escHtml(p.videoCodec)}</span>` : ""}
          ${p.audioCodec ? `<span>音频 ${escHtml(p.audioCodec)} · ${p.sampleRate / 1000}kHz</span>` : ""}
        </div>
      </div>
    </div>
    ${!p.hasAudio ? `<div class="alert alert-warning" style="margin-top:12px"><span class="ic">${icon("alert", 18)}</span><div><strong>这个文件没有音轨</strong>请换一个带声音的文件，或先用视频转码提取音频。</div></div>` : ""}
  `;
  main.appendChild(mediaCard);

  // 参数卡片
  const optionsCard = h("div", { class: "card card-pad" });

  // 引擎状态提示
  if (!engineReady) {
    optionsCard.appendChild(h("div", { class: "alert alert-warning", style: "margin-bottom:14px" },
      h("span", { class: "ic", html: icon("alert", 18) }),
      h("div", {},
        h("strong", {}, "本地识别引擎还没准备好"),
        h("span", {}, (state.env?.problems || ["缺少模型或依赖"])[0]),
        h("div", { style: "margin-top:6px" },
          h("a", { class: "btn btn-xs btn-soft", href: "#/models" }, "去模型中心"),
          h("a", { class: "btn btn-xs btn-ghost", href: "#/settings", style: "margin-left:6px" }, "检查环境"),
        ),
      ),
    ));
  }

  // 识别模型
  const modelGroup = h("div", { class: "option-group" });
  modelGroup.innerHTML = `<h4>识别模型</h4>`;
  const modelRow = h("div", { class: "row" });
  const modelField = h("div", { class: "field" });
  modelField.innerHTML = `<label>模型</label>`;
  const modelSelect = h("select", { class: "select" });
  const autoOpt = h("option", { value: "" }, "自动选择（推荐）");
  modelSelect.appendChild(autoOpt);
  modelList.forEach((m) => {
    const label = prettyModel(m.id) + ` · ${prettySize(m.sizeBytes)}`;
    const opt = h("option", { value: m.id }, label);
    modelSelect.appendChild(opt);
  });
  if (stateObj.modelId) {
    const match = modelList.find((m) => m.id === stateObj.modelId || m.id.startsWith(stateObj.modelId));
    if (match) modelSelect.value = match.id;
  }
  modelField.appendChild(modelSelect);
  modelSelect.addEventListener("change", () => { stateObj.modelId = modelSelect.value; });
  modelRow.appendChild(modelField);

  const langField = h("div", { class: "field" });
  langField.innerHTML = `<label>语言</label>`;
  const languageSelect = h("select", { class: "select", id: "transcribe-language" });
  LANGS.forEach(([value, label]) => languageSelect.appendChild(h("option", { value }, label)));
  if (stateObj.language) languageSelect.value = stateObj.language;
  languageSelect.addEventListener("change", () => { stateObj.language = languageSelect.value; });
  langField.appendChild(languageSelect);
  modelRow.appendChild(langField);
  modelGroup.appendChild(modelRow);
  optionsCard.appendChild(modelGroup);

  // 字幕风格
  const styleGroup = h("div", { class: "option-group" });
  styleGroup.innerHTML = `<h4>字幕风格 <span class="muted small" style="font-weight:400">· 决定断句与折行方式</span></h4>`;
  const chips = h("div", { class: "preset-chips" });
  Object.entries(PRESETS).forEach(([key, preset]) => {
    const chip = h("button", { class: "preset-chip" + (key === stateObj.preset ? " active" : ""), type: "button", title: preset.hint }, preset.label);
    chip.addEventListener("click", () => {
      stateObj.preset = key;
      chips.querySelectorAll(".preset-chip").forEach((c) => c.classList.remove("active"));
      chip.classList.add("active");
      hint.textContent = preset.hint;
    });
    chips.appendChild(chip);
  });
  styleGroup.appendChild(chips);
  const hint = h("div", { class: "field-hint", style: "margin-top:8px" }, PRESETS[stateObj.preset].hint);
  styleGroup.appendChild(hint);
  optionsCard.appendChild(styleGroup);

  // 输出格式
  const formatGroup = h("div", { class: "option-group" });
  formatGroup.innerHTML = `<h4>输出格式</h4>`;
  const formatRow = h("div", { class: "cluster" });
  const formats = [["srt", "SRT", "通用字幕格式"], ["vtt", "VTT", "网页播放器"], ["json", "JSON", "含时间轴数据"]];
  formats.forEach(([value, label, title]) => {
    const btn = h("button", {
      class: "preset-chip" + (stateObj.format === value ? " active" : ""),
      type: "button", title,
    }, label);
    btn.addEventListener("click", () => {
      stateObj.format = value;
      formatRow.querySelectorAll(".preset-chip").forEach((c) => c.classList.remove("active"));
      btn.classList.add("active");
    });
    formatRow.appendChild(btn);
  });
  formatGroup.appendChild(formatRow);
  optionsCard.appendChild(formatGroup);

  // 操作按钮
  const actions = h("div", { class: "cluster", style: "margin-top:18px" });
  const saveToggle = h("label", { class: "check" },
    h("input", { type: "checkbox", checked: Boolean(project.favorite) }),
    h("span", {}, "保存到项目库"),
  );
  saveToggle.hidden = !project.temporary;
  const startBtn = h("button", {
    class: "btn btn-primary btn-lg", type: "button", disabled: !engineReady || !p.hasAudio,
  });
  startBtn.innerHTML = icon("wave", 18) + " 开始转写";
  actions.appendChild(startBtn);
  actions.appendChild(saveToggle);
  optionsCard.appendChild(actions);
  main.appendChild(optionsCard);

  // 进度 / 结果区域
  const runHost = h("div", {});
  main.appendChild(runHost);

  // ---------- 右侧 ----------
  const side = h("div", { class: "transcribe-side" });

  const tipCard = h("div", { class: "card card-pad" });
  tipCard.innerHTML = `
    <h4 style="margin:0 0 10px;font-size:12.5px;font-weight:600;color:var(--text-2)">转写说明</h4>
    <div class="stack-sm small muted">
      <div>· 任务在后台运行，可以切到别的页面，回来仍能看到进度</div>
      <div>· 长音频会自动切分并尽量切在停顿处</div>
      <div>· 关闭程序后任务会标记为「已中断」，可一键重试</div>
      <div>· 识别结果会保存为项目字幕，可随时编辑</div>
    </div>
  `;
  side.appendChild(tipCard);

  if (project.subtitles?.length) {
    const listCard = h("div", { class: "card card-pad" });
    listCard.innerHTML = `<h4 style="margin:0 0 10px;font-size:12.5px;font-weight:600;color:var(--text-2)">已有字幕</h4>`;
    const list = h("div", { class: "list" });
    project.subtitles.forEach((track) => {
      const item = h("div", { class: "list-item" });
      item.innerHTML = `
        <div class="grow">
          <div class="title">${escHtml(track.fileName || track.format.toUpperCase())}</div>
          <div class="meta"><span>${track.cueCount} 条</span><span>${track.format.toUpperCase()}</span><span>${track.edited ? "已编辑" : "自动生成"}</span></div>
        </div>
      `;
      const edit = h("a", { class: "btn btn-xs btn-ghost", href: `#/editor/${project.id}?track=${track.id}` }, "编辑");
      item.appendChild(edit);
      list.appendChild(item);
    });
    listCard.appendChild(list);
    side.appendChild(listCard);
  }

  page.append(main, side);
  element.appendChild(page);

  // ---------- 行为 ----------
  const updateJob = (job) => {
    if (!job) return;
    stateObj.currentJob = job.id;
    const pct = Math.round((job.overall || 0) * 100);
    if (job.status === "running" || job.status === "pending") {
      startBtn.disabled = true;
      renderProgress(runHost, job, pct);
    } else {
      startBtn.disabled = false;
    }
  };

  const showError = (job) => {
    runHost.replaceChildren();
    const card = h("div", { class: "card card-pad" });
    card.innerHTML = `
      <div class="alert alert-danger">
        <span class="ic">${icon("alert", 18)}</span>
        <div>
          <strong>转写失败</strong>
          ${escHtml(job.error || "未知错误")}
          ${job.errorHint ? `<div class="small" style="margin-top:6px;opacity:.85">${escHtml(job.errorHint)}</div>` : ""}
        </div>
      </div>
      <div class="cluster" style="margin-top:12px">
        <button class="btn btn-soft btn-sm retry">重试</button>
        <a class="btn btn-ghost btn-sm" href="#/settings">检查设置</a>
      </div>
    `;
    card.querySelector(".retry").addEventListener("click", () => start());
    runHost.appendChild(card);
  };

  const start = async () => {
    if (stateObj.currentJob) return;
    stateObj.finished = false;
    startBtn.disabled = true;
    runHost.replaceChildren(progressSkeleton());
    // 临时项目：若勾选保存则转为正式项目。
    if (project.temporary && saveToggle.querySelector("input").checked) {
      try {
        await fetch(`/api/projects/${project.id}`, {
          method: "PATCH",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ temporary: false }),
        });
      } catch (_) { /* 忽略，任务仍可继续 */ }
    }
    const preset = PRESETS[stateObj.preset].sub;
    try {
      const payload = await post("/api/jobs", {
        type: "transcribe",
        projectId: project.id,
        params: {
          format: stateObj.format,
          language: stateObj.language,
          modelId: stateObj.modelId === (state.settings?.modelId || "") ? "" : stateObj.modelId,
          subtitleId: stateObj.subtitleId || "",
        },
      });
      const job = payload.jobs?.[0];
      if (job) {
        stateObj.currentJob = job.id;
        updateJob(job);
      }
    } catch (err) {
      startBtn.disabled = false;
      runHost.replaceChildren();
      toast("无法启动任务", err.message || String(err), "error");
    }
  };

  startBtn.addEventListener("click", start);

  const appendLogs = (lines) => {
    const log = runHost.querySelector(".run-log");
    if (!log) return;
    log.textContent = lines.slice(-30).join("\n");
    log.scrollTop = log.scrollHeight;
  };

  return { element, state: stateObj, updateJob, appendLogs, showError, languageSelect };
}

function handleSuccess(job, page, projectId) {
  const res = job.result || {};
  const target = projectId || job.projectId;
  const host = page.element.querySelector(".transcribe-layout");
  const runHost = host?.querySelector(".transcribe-main > div:last-child");
  if (!runHost) return;
  page.updateJob(job);
  const card = h("div", { class: "card card-pad" });
  const fileName = "字幕";
  card.innerHTML = `
    <div class="alert alert-success">
      <span class="ic">${icon("check", 18)}</span>
      <div><strong>转写完成</strong>${escHtml(res.message || "")}${res.detectedLanguage ? ` · 检测语言：${escHtml(res.detectedLanguage)}` : ""}</div>
    </div>
    <div class="cluster" style="margin-top:14px">
      <a class="btn btn-primary btn-sm" href="#/editor/${target}?track=${res.subtitleId || ""}">打开字幕编辑器</a>
      <a class="btn btn-soft btn-sm" href="/api/projects/${target}/exports?format=srt&track=${res.subtitleId || ""}">下载 SRT</a>
      <a class="btn btn-ghost btn-sm" href="#/project/${target}">项目详情</a>
    </div>
  `;
  runHost.replaceChildren(card);
  toast("转写完成", res.message || "", "success");
}

function renderProgress(host, job, pct) {
  let card = host.querySelector(".run-panel");
  if (!card) {
    host.replaceChildren(progressSkeleton(job, pct));
    return;
  }
  card.querySelector(".pct").textContent = pct + "%";
  card.querySelector(".run-ring svg").outerHTML = progressRing(pct / 100);
  card.querySelector(".run-meta strong").textContent = jobTitle(job);
  card.querySelector(".run-meta span").textContent = job.phase || "处理中";
  const bar = card.querySelector(".progress > i");
  if (bar) bar.style.width = pct + "%";
}

function progressSkeleton(job, pct = 0) {
  const card = h("div", { class: "card card-pad run-panel" });
  card.innerHTML = `
    <div class="run-head">
      <div class="run-ring">${progressRing(pct / 100)}<span class="pct">${pct}%</span></div>
      <div class="run-meta">
        <strong>${job ? jobTitle(job) : "正在启动任务…"}</strong>
        <span>${job?.phase || "准备中"}</span>
      </div>
      <button class="btn btn-xs btn-danger-soft cancel" type="button">取消</button>
    </div>
    <div class="progress striped"><i style="width:${pct}%"></i></div>
    <div class="run-log">等待日志输出…</div>
  `;
  card.querySelector(".cancel").addEventListener("click", async (ev) => {
    if (!job?.id) return;
    ev.target.disabled = true;
    try {
      await post(`/api/jobs/${job.id}/cancel`);
      toast("已请求取消", "任务会在当前片段结束后停止", "warn");
    } catch (err) {
      toast("取消失败", err.message, "error");
      ev.target.disabled = false;
    }
  });
  return card;
}

function jobTitle(job) {
  return job.title || "转写任务";
}

function renderImportPrompt() {
  const wrap = h("div", { class: "stack" });
  wrap.innerHTML = `
    <div class="page-head">
      <div><h1>字幕转写</h1><p class="sub">先导入一个音视频文件，再选择识别参数。</p></div>
    </div>
  `;
  const zone = h("div", { class: "dropzone", role: "button", tabindex: "0" });
  zone.innerHTML = `
    <div class="dz-icon">${icon("upload", 26)}</div>
    <strong>选择音视频文件</strong>
    <p>导入后即可开始转写；也可以直接把文件拖到窗口任意位置</p>
  `;
  zone.addEventListener("click", () => pickFile({ route: "transcribe", temporary: false }));
  zone.addEventListener("keydown", (ev) => {
    if (ev.key === "Enter" || ev.key === " ") pickFile({ route: "transcribe", temporary: false });
  });
  wrap.appendChild(zone);
  return wrap;
}

function prettyModel(id) {
  if (!id) return "模型";
  if (/sense-voice/i.test(id)) return "SenseVoice";
  if (/paraformer/i.test(id)) return "Paraformer";
  if (/reazon/i.test(id)) return "ReazonSpeech";
  if (/whisper/i.test(id)) return "Whisper";
  return id;
}

function prettySize(n) {
  if (!n) return "";
  const gb = n / 1024 / 1024 / 1024;
  if (gb >= 1) return gb.toFixed(1) + " GB";
  return Math.round(n / 1024 / 1024) + " MB";
}



