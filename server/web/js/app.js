// 入口：主题、路由、全局导入、任务中心、引擎状态。
import { get, post, upload, subscribeJobs } from "./api.js";
import { h, debounce } from "./dom.js";
import { toast, initTheme, icon, menuPopup, openModal, confirmDialog, bytes, duration, dateTime, escHtml } from "./ui.js";
import { openPlaylist } from "./player.js";
import {
  state, loadSystem, refreshStats, refreshProjects, refreshFolders, refreshCollections, mergeJobs, onState,
  setActiveProject, restoreActiveProject, reloadActiveProject,
} from "./store.js";
import dashboard from "./pages/dashboard.js";
import transcribe from "./pages/transcribe.js";
import projects, { renderDetail as renderProjectDetail } from "./pages/projects.js";
import editor from "./pages/editor.js";
import transcode from "./pages/transcode.js";
import analyze from "./pages/analyze.js";
import models from "./pages/models.js";
import settingsPage from "./pages/settings.js";
import playerPage from "./pages/player.js";

const ROUTES = {
  dashboard, transcribe, projects, editor, transcode, analyze, models,
  settings: settingsPage,
  player: playerPage,
};

const TITLES = {
  dashboard: "总览", transcribe: "字幕转写", projects: "项目库", editor: "字幕编辑",
  transcode: "视频转码", analyze: "媒体分析", models: "模型中心", settings: "设置",
  project: "项目详情", player: "播放",
};

let currentCleanup = null;
const view = () => document.getElementById("view");

// 这些页面在没有指定项目时，会自动复用侧边栏的「当前媒体」。
const REUSE_ACTIVE = ["transcribe", "transcode", "analyze", "editor", "player"];
// 这些路由会把对应项目设为当前媒体。
const TRACK_ACTIVE = ["project", "player", "transcribe", "transcode", "analyze", "editor"];

// ---------- 路由 ----------
function parseHash() {
  const raw = (location.hash || "#/dashboard").replace(/^#\/?/, "");
  const [path, query = ""] = raw.split("?");
  const parts = path.split("/").filter(Boolean);
  return { name: parts[0] || "dashboard", id: parts[1] || "", query: new URLSearchParams(query) };
}

async function render() {
  const { name, id, query } = parseHash();

  // 没有带项目 ID 的功能页：如果选过媒体，直接沿用，避免每次都要重新选择/上传。
  if (REUSE_ACTIVE.includes(name) && !id) {
    const cur = state.activeProject;
    if (cur?.id) {
      const qs = query.toString();
      location.replace(`#/${name}/${cur.id}${qs ? "?" + qs : ""}`);
      return;
    }
  }

  const page = name === "project" ? null : ROUTES[name] || dashboard;
  if (typeof currentCleanup === "function") {
    try { currentCleanup(); } catch (_) { /* 忽略 */ }
    currentCleanup = null;
  }
  view().scrollTop = 0;
  updateNav(name === "project" || name === "player" ? "projects" : name);
  updateCrumbs(name, id);
  document.title = `${TITLES[name] || "视听工作台"} · 视听工作台`;

  // 记录当前媒体（异步，不阻塞页面渲染）。
  if (id && TRACK_ACTIVE.includes(name)) {
    setActiveProject(id).then(() => renderNowMedia()).catch(() => {});
  }

  try {
    if (name === "project") {
      if (!id) {
        location.hash = "#/projects";
        return;
      }
      const cleanup = await renderProjectDetail(view(), id);
      if (typeof cleanup === "function") currentCleanup = cleanup;
      return;
    }
    const cleanup = await page.render(view(), { id, query, name });
    if (typeof cleanup === "function") currentCleanup = cleanup;
  } catch (err) {
    console.error(err);
    view().replaceChildren(
      h("div", { class: "card card-pad" },
        h("div", { class: "alert alert-danger" },
          h("span", { class: "ic", html: icon("alert", 18) }),
          h("div", {}, h("strong", {}, "页面加载失败"), err.message || String(err)),
        ),
      ),
    );
  }
}

function updateNav(name) {
  document.querySelectorAll(".nav-item[data-nav]").forEach((el) => {
    el.classList.toggle("active", el.dataset.nav === name);
  });
  document.getElementById("sidebar")?.classList.remove("open");
}

function updateCrumbs(name, id) {
  const host = document.getElementById("crumbs");
  if (!host) return;
  if ((name === "project" || name === "player") && id) {
    const label = name === "player" ? "播放" : "项目详情";
    host.innerHTML = `<a href="#/projects">项目库</a><span class="sep">/</span><strong>${label}</strong>`;
    return;
  }
  host.innerHTML = `<strong>${TITLES[name] || "视听工作台"}</strong>`;
}

// ---------- 批量任务 ----------
async function submitBatch(type, ids) {
  if (!ids.length) { toast("没有可处理的媒体", "", "warn"); return; }
  try {
    const res = await post("/api/jobs", { type, projectIds: ids });
    const ok = res.jobs?.length || 0;
    const skip = res.skipped?.length || 0;
    toast(`已提交 ${ok} 个${type === "transcribe" ? "转写" : "转码"}任务`, skip ? `跳过 ${skip} 个` : "", "success", 4200);
  } catch (err) { toast("提交失败", err.message, "error"); }
}

// 供项目列表多选批量操作复用。
export async function batchProjects(type, ids) { return submitBatch(type, ids); }

// ---------- 全局导入 ----------
let importing = false;

export async function importFile(file, options = {}) {
  const kind = options.kind || "transcribe";
  const temporary = options.temporary !== false;
  if (!file) return null;
  if (importing) {
    toast("正在导入", "请等待当前文件导入完成", "warn");
    return null;
  }
  importing = true;
  const t = toast("正在导入", `${file.name} · 准备中…`, "info", 0);
  try {
    const payload = await upload("/api/ingest", file, {
      title: file.name.replace(/\.[^.]+$/, ""),
      kind,
      temporary: temporary ? "1" : "0",
    }, {
      onProgress: (loaded, total) => {
        const pct = total ? Math.round((loaded / total) * 100) : 0;
        const span = t.node?.querySelector(".content span");
        if (span) span.textContent = `${file.name} · ${pct}%`;
      },
    });
    t.dismiss();
    toast("导入完成", payload.project?.title || file.name, "success", 2600);
    await refreshStats();
    // 导入后即成为「当前媒体」，后续转写/转码/分析都会默认用它。
    if (payload.project) {
      await setActiveProject(payload.project).catch(() => {});
      renderNowMedia();
    }
    return payload;
  } catch (err) {
    t.dismiss();
    toast("导入失败", err.message || String(err), "error", 6000);
    if (err.hint) setTimeout(() => toast("处理建议", err.hint, "info", 7000), 400);
    return null;
  } finally {
    importing = false;
  }
}

// 选择文件 -> 导入 -> 跳转
export function pickFile(options = {}) {
  const input = document.getElementById("hidden-file");
  input.value = "";
  input.onchange = async () => {
    const file = input.files?.[0];
    if (!file) return;
    const payload = await importFile(file, options);
    if (payload?.project?.id) {
      const target = options.route || "transcribe";
      location.hash = `#/${target}/${payload.project.id}`;
      if (typeof options.onImported === "function") options.onImported(payload);
    }
  };
  input.click();
}

function bindGlobalDrop() {
  const overlay = document.getElementById("drop-overlay");
  let depth = 0;
  window.addEventListener("dragenter", (ev) => {
    if (!ev.dataTransfer?.types?.includes("Files")) return;
    ev.preventDefault();
    depth += 1;
    overlay.classList.add("show");
  });
  window.addEventListener("dragover", (ev) => {
    if (ev.dataTransfer?.types?.includes("Files")) ev.preventDefault();
  });
  window.addEventListener("dragleave", () => {
    depth = Math.max(0, depth - 1);
    if (depth === 0) overlay.classList.remove("show");
  });
  window.addEventListener("drop", async (ev) => {
    if (!ev.dataTransfer?.files?.length) return;
    ev.preventDefault();
    depth = 0;
    overlay.classList.remove("show");
    const file = ev.dataTransfer.files[0];
    const payload = await importFile(file);
    if (payload?.project?.id) {
      location.hash = `#/transcribe/${payload.project.id}`;
    }
  });
}

// ---------- 任务中心 ----------
let drawerOpen = false;

function bindJobsDrawer() {
  const drawer = document.getElementById("jobs-drawer");
  const mask = document.getElementById("jobs-mask");
  const open = () => {
    drawerOpen = true;
    drawer.classList.add("open");
    drawer.setAttribute("aria-hidden", "false");
    mask.hidden = false;
    requestAnimationFrame(() => mask.classList.add("show"));
    renderJobs();
  };
  const close = () => {
    drawerOpen = false;
    drawer.classList.remove("open");
    drawer.setAttribute("aria-hidden", "true");
    mask.classList.remove("show");
    setTimeout(() => { mask.hidden = true; }, 180);
  };
  document.getElementById("jobs-toggle").addEventListener("click", () => (drawerOpen ? close() : open()));
  document.getElementById("jobs-close").addEventListener("click", close);
  mask.addEventListener("click", close);
}

function renderJobs() {
  const host = document.getElementById("jobs-list");
  if (!host) return;
  const jobs = state.jobs.slice(0, 40);
  if (!jobs.length) {
    host.innerHTML = `<div class="empty">${icon("wave", 40)}<strong>还没有任务</strong><p>导入媒体并开始转写后，这里会显示实时进度。</p></div>`;
    return;
  }
  host.replaceChildren(...jobs.map(jobCard));
}

function jobCard(job) {
  const pct = Math.round((job.overall || 0) * 100);
  const running = job.status === "running" || job.status === "pending";
  const statusTag = {
    running: `<span class="tag tag-accent">运行中</span>`,
    pending: `<span class="tag">排队中</span>`,
    succeeded: `<span class="tag tag-success">已完成</span>`,
    failed: `<span class="tag tag-danger">失败</span>`,
    canceled: `<span class="tag">已取消</span>`,
    interrupted: `<span class="tag tag-warning">已中断</span>`,
  }[job.status] || "";

  const card = h("div", { class: "card", style: "padding:12px;margin-bottom:10px" });
  card.innerHTML = `
    <div style="display:flex;align-items:flex-start;gap:10px">
      <div style="flex:1;min-width:0">
        <div style="display:flex;gap:8px;align-items:center;margin-bottom:2px">
          <strong style="font-size:13px;font-weight:600;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">${escapeHtml(job.title || job.type)}</strong>
          ${statusTag}
        </div>
        <div class="small muted" style="margin-bottom:8px">${escapeHtml(job.phase || "等待开始")}${running ? ` · ${pct}%` : ""}</div>
      </div>
    </div>
    <div class="progress thin ${job.status === "failed" ? "danger" : job.status === "succeeded" ? "success" : "striped"}"><i style="width:${pct}%"></i></div>
    ${job.error ? `<div class="small" style="color:var(--danger);margin-top:8px">${escapeHtml(job.error)}</div>` : ""}
    <div class="actions" style="display:flex;gap:6px;margin-top:10px;flex-wrap:wrap"></div>
  `;
  const actions = card.querySelector(".actions");
  if (job.projectId) {
    actions.appendChild(h("button", {
      class: "btn btn-xs btn-ghost", type: "button",
      onclick: () => { location.hash = `#/project/${job.projectId}`; document.getElementById("jobs-mask")?.click(); },
    }, "查看项目"));
  }
  if (running) {
    actions.appendChild(h("button", {
      class: "btn btn-xs btn-danger-soft", type: "button",
      onclick: async (ev) => {
        ev.target.disabled = true;
        try {
          await post(`/api/jobs/${job.id}/cancel`);
          toast("已请求取消", job.title || "", "warn");
        } catch (err) {
          toast("取消失败", err.message, "error");
        }
      },
    }, "取消"));
  } else if (job.status === "failed" || job.status === "interrupted" || job.status === "canceled") {
    actions.appendChild(h("button", {
      class: "btn btn-xs btn-soft", type: "button",
      onclick: async (ev) => {
        ev.target.disabled = true;
        try {
          await post(`/api/jobs/${job.id}/retry`);
          toast("已重新开始", job.title || "", "success");
        } catch (err) {
          toast("重试失败", err.message, "error");
        }
      },
    }, "重试"));
    actions.appendChild(h("button", {
      class: "btn btn-xs btn-ghost", type: "button",
      onclick: () => showJobLog(job),
    }, "日志"));
  }
  return card;
}

async function showJobLog(job) {
  try {
    const data = await get(`/api/jobs/${job.id}/logs`);
    const lines = (data.logs || []).join("\n") || "（暂无日志）";
    const { openModal } = await import("./ui.js");
    openModal({
      title: "任务日志",
      wide: true,
      content: h("pre", { class: "code", style: "max-height:52vh" }, lines),
    });
  } catch (err) {
    toast("读取日志失败", err.message, "error");
  }
}

function escapeHtml(value) {
  return String(value ?? "").replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
}

// ---------- 引擎状态 ----------
function renderEngine() {
  const chip = document.getElementById("engine-chip");
  const label = document.getElementById("engine-label");
  if (!chip || !state.env) return;
  const env = state.env;
  chip.classList.remove("warn", "error");
  if (env.ready) {
    const models = env.installedModels || [];
    // 与后端一致的优先级：SenseVoice > Paraformer > Zipformer > Whisper。
    const rank = (id) => (/sense-voice/i.test(id) ? 4 : /paraformer/i.test(id) ? 3 : /reazon|zipformer/i.test(id) ? 2 : /whisper/i.test(id) ? 1 : 0);
    const best = models.slice().sort((a, b) => rank(b.id) - rank(a.id))[0];
    label.textContent = `本地引擎就绪${best ? " · " + prettyModel(best.id) : ""}`;
    label.title = env.python?.version || "";
  } else if (env.ffmpeg?.ok) {
    chip.classList.add("warn");
    label.textContent = "本地引擎缺少依赖";
  } else {
    chip.classList.add("error");
    label.textContent = "缺少 ffmpeg";
  }
}

function prettyModel(id) {
  if (!id) return "";
  if (/sense-voice/i.test(id)) return "SenseVoice";
  if (/paraformer/i.test(id)) return "Paraformer";
  if (/reazon/i.test(id)) return "ReazonSpeech";
  if (/whisper/i.test(id)) return "Whisper";
  return id.split("-").slice(0, 2).join("-");
}

// ---------- 当前媒体（侧边栏） ----------
function renderNowMedia() {
  const host = document.getElementById("now-media");
  if (!host) return;
  const p = state.activeProject;
  if (!p) {
    host.innerHTML = `
      <div class="now-label">当前媒体</div>
      <div class="now-empty">
        还没有载入媒体。用「换碟」从项目库挑选，或直接导入新文件。
        <div class="now-empty-actions">
          <button type="button" data-role="swap">${icon("shuffle", 13)}<span>换碟</span></button>
          <button type="button" data-role="import">${icon("upload", 13)}<span>导入</span></button>
        </div>
      </div>`;
    host.querySelector("[data-role=swap]")?.addEventListener("click", openSwapDialog);
    host.querySelector("[data-role=import]")?.addEventListener("click", () => pickFile({ route: "player", temporary: false }));
    return;
  }
  const probe = p.media?.probe || {};
  const cover = probe.hasVideo
    ? `background-image:url(/api/projects/${p.id}/thumbnail?at=1)`
    : "";
  const subs = (p.subtitles || []).length;
  const job = state.jobs.find((j) => j.projectId === p.id && (j.status === "running" || j.status === "pending"));
  const meta = job
    ? `处理中 · ${Math.round((job.overall || 0) * 100)}%`
    : `${duration(p.duration)}${subs ? ` · ${subs} 份字幕` : " · 未转写"}`;
  host.innerHTML = `
    <div class="now-label">当前媒体</div>
    <a class="now-card" href="#/player/${p.id}" title="打开播放页">
      <span class="now-thumb" style="${cover}">${probe.hasVideo ? "" : icon("audio", 16)}</span>
      <span class="now-info">
        <strong>${escHtml(p.title)}</strong>
        <span>${meta}</span>
      </span>
    </a>
    <div class="now-actions">
      <button type="button" data-role="swap" title="换碟：从项目库切换当前媒体">${icon("shuffle", 14)}<span>换碟</span></button>
      <button type="button" data-role="refresh" title="刷新当前媒体的字幕与任务状态">${icon("refresh", 14)}<span>刷新</span></button>
      <button type="button" data-role="play" title="播放当前媒体">${icon("play", 14)}<span>播放</span></button>
    </div>`;
  host.querySelector("[data-role=swap]")?.addEventListener("click", () => openSwapDialog());
  host.querySelector("[data-role=refresh]")?.addEventListener("click", () => refreshNowMedia());
  host.querySelector("[data-role=play]")?.addEventListener("click", () => { location.hash = `#/player/${p.id}`; });
}

/** 换碟：从项目库挑选已有媒体，或导入新文件。 */
async function openSwapDialog() {
  let items = [];
  try {
    // 项目列表默认只返回非临时项目，正好用作「碟架」。
    const data = await get("/api/projects?limit=200&sort=recent");
    items = data.projects || [];
  } catch (err) {
    toast("读取项目库失败", err.message || String(err), "error");
    return;
  }

  const currentId = state.activeProject?.id || "";
  // 当前媒体如果是临时工作区（不在项目库），也放进碟架，避免「当前媒体」在列表里找不到。
  if (state.activeProject && !items.some((it) => it.id === state.activeProject.id)) {
    items = [state.activeProject, ...items];
  }
  let modal = null;
  const pick = (project) => {
    modal?.close();
    swapTo(project);
  };

  const list = h("div", { class: "disk-list" });
  const search = h("input", {
    class: "input input-sm disk-search",
    type: "search",
    placeholder: "搜索标题或文件名…",
    "aria-label": "搜索媒体",
  });
  const paint = (keyword = "") => {
    const q = keyword.trim().toLowerCase();
    const shown = q
      ? items.filter((it) => `${it.title || ""} ${it.media?.fileName || ""}`.toLowerCase().includes(q))
      : items;
    list.replaceChildren();
    if (!shown.length) {
      list.append(h("div", { class: "disk-empty" },
        q ? "没有匹配的媒体" : "项目库还是空的，先导入一个文件吧。"));
      return;
    }
    for (const it of shown) list.append(diskRow(it, currentId, pick));
  };
  paint();
  search.addEventListener("input", debounce(() => paint(search.value), 160));

  modal = openModal({
    title: "换碟 · 选择媒体",
    wide: true,
    content: h("div", { class: "disk-panel" }, search, list),
    footer: h("div", { class: "disk-foot" },
      h("button", { class: "btn btn-ghost", type: "button", onclick: () => modal?.close() }, "关闭"),
      h("button", {
        class: "btn btn-primary", type: "button",
        html: `${icon("upload", 15)} 导入新文件`,
        onclick: () => { modal?.close(); pickFile({ route: "player", temporary: false }); },
      }),
    ),
  });
}

/** 碟架里的一行：缩略图 + 标题 + 时长/字幕数。 */
function diskRow(project, currentId, onPick) {
  const probe = project.media?.probe || {};
  const subs = (project.subtitles || []).length;
  const thumb = h("span", { class: "disk-thumb", html: probe.hasVideo ? "" : icon("audio", 15) });
  if (probe.hasVideo) thumb.style.backgroundImage = `url(/api/projects/${project.id}/thumbnail?at=1)`;
  const facts = [
    duration(project.duration),
    subs ? `${subs} 份字幕` : "未转写",
    dateTime(project.updatedAt),
  ].filter(Boolean).join(" · ");
  return h("button", {
    type: "button",
    class: `disk-item${project.id === currentId ? " active" : ""}`,
    title: project.title || project.id,
    onclick: () => onPick(project),
  },
    thumb,
    h("span", { class: "disk-info" },
      h("strong", {}, project.title || project.media?.fileName || project.id),
      h("span", {}, facts),
    ),
    project.id === currentId
      ? h("span", { class: "disk-tag" }, "当前")
      : project.temporary
        ? h("span", { class: "disk-tag plain" }, "临时")
        : h("span", { class: "disk-go", html: icon("arrowRight", 15) }),
  );
}

/** 切换当前媒体：保留当前功能页（转写/编辑/播放…），没有则进入播放页。 */
async function swapTo(project) {
  const { name } = parseHash();
  await setActiveProject(project).catch(() => null);
  renderNowMedia();
  toast("已换碟", project.title || project.media?.fileName || "", "success", 2200);
  const route = REUSE_ACTIVE.includes(name) ? name : "player";
  const target = `#/${route}/${project.id}`;
  // 目标与当前地址相同不会触发 hashchange，手动重渲染。
  if (location.hash === target) await render();
  else location.hash = target;
}

/** 刷新当前媒体：重新拉取项目数据（字幕、成片、任务状态），并同步概览统计。 */
async function refreshNowMedia() {
  const cur = state.activeProject;
  if (!cur?.id) {
    toast("还没有当前媒体", "先用「换碟」选择或导入文件", "warn");
    return;
  }
  try {
    const latest = await reloadActiveProject();
    renderNowMedia();
    await Promise.all([refreshStats(), refreshProjects()]);
    const { name, id } = parseHash();
    // 当前页面本身就是这个项目时，重渲染让字幕/输出列表同步更新。
    if (id === (latest?.id || cur.id) && name !== "dashboard") await render();
    toast("已刷新", latest?.title || cur.title, "success", 2200);
  } catch (err) {
    toast("刷新失败", err.message || String(err), "error");
  }
}

/** 任务进度推送时轻量刷新侧边栏（避免每帧重建）。 */
const refreshNowMediaSoon = debounce(renderNowMedia, 500);

// ---------- 启动 ----------
async function boot() {
  try {
    await loadSystem();
  } catch (err) {
    document.getElementById("view").innerHTML = `
      <div class="card card-pad"><div class="alert alert-danger">
        <strong>无法连接本地服务</strong>请确认 stt-sub.exe 仍在运行，然后刷新页面。
      </div></div>`;
    return;
  }
  // 恢复上次使用的媒体，保证刷新后「当前媒体」还在。
  await restoreActiveProject().catch(() => {});
  // 先拿到文件夹列表，播放页/项目库才能显示「位置」。
  await refreshFolders().catch(() => {});
  initTheme(state.settings?.theme || "auto");
  document.getElementById("app-version").textContent = "v" + (state.system?.version || "3.0.0");
  renderEngine();
  renderNowMedia();
  bindGlobalDrop();
  bindJobsDrawer();
  document.getElementById("quick-drop-btn").addEventListener("click", () => pickFile());
  document.getElementById("menu-toggle").addEventListener("click", () => {
    document.getElementById("sidebar").classList.toggle("open");
  });

  window.addEventListener("hashchange", render);
  if (!location.hash) location.hash = "#/dashboard";
  await render();

  if (state.env && !state.env.ready) {
    setTimeout(() => {
      const problems = state.env.problems || [];
      if (problems.length) toast("准备工作未完成", problems[0], "warn", 9000);
    }, 800);
  }

  subscribeJobs((payload) => {
    mergeJobs(payload);
    updateJobBadge();
    if (drawerOpen) renderJobs();
    document.dispatchEvent(new CustomEvent("jobs:update", { detail: payload }));
    debounce(refreshStats, 600)();
    refreshNowMediaSoon();
    // 任务完成后刷新「当前媒体」，字幕数/成片数会变化。
    const cur = state.activeProject;
    const finished = (payload?.jobs || []).some((j) => j.projectId === cur?.id && !["running", "pending"].includes(j.status));
    if (cur && finished) {
      debounce(() => setActiveProject(cur.id).then(renderNowMedia).catch(() => {}), 800)();
    }
  });

  setInterval(() => {
    updateJobBadge();
    if (drawerOpen) renderJobs();
  }, 5000);

  refreshStats().then(updateJobBadge);
  refreshProjects().then(() => updateProjectBadge());
  refreshCollections();
  onState((s, scope) => {
    if (scope === "projects") updateProjectBadge();
    if (scope === "system") renderEngine();
    if (scope === "active" || scope === "stats") renderNowMedia();
  });
}

function updateJobBadge() {
  const badge = document.getElementById("jobs-badge");
  if (!badge) return;
  badge.hidden = (state.activeJobs || 0) === 0;
}

function updateProjectBadge() {
  const badge = document.getElementById("nav-project-count");
  if (!badge) return;
  // 优先用资料库总数（项目库视图可能经过筛选）。
  const n = state.counts?.total ?? state.projects.length;
  badge.hidden = n === 0;
  badge.textContent = n > 99 ? "99+" : String(n);
}

boot();

