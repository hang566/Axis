// 全局状态与数据加载：页面共享同一份快照，任务事件统一在这里合并。
import { get } from "./api.js";

export const state = {
  system: null,       // { name, version, dataDir, modelsDir, settings, ... }
  settings: null,
  defaults: null,
  env: null,          // 环境自检
  stats: null,        // { library, activeJobs, recentJobs, projects }
  projects: [],       // 项目库列表
  jobs: [],           // 最近任务
  activeJobs: 0,
  models: null,
  ready: false,
  folders: [],
  trashFolders: [],
  counts: null,
  collections: [],
  // 当前媒体：跨页面共享，切换功能/刷新后仍然保留。
  activeProject: null,
};

const ACTIVE_KEY = "stt.activeProject";

const listeners = new Set();

/** 订阅状态变化，返回取消函数 */
export function onState(fn) {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

function emit(scope) {
  for (const fn of listeners) {
    try { fn(state, scope); } catch (err) { console.error(err); }
  }
}

export async function loadSystem() {
  const [system, settings, env] = await Promise.all([
    get("/api/system"),
    get("/api/settings").catch(() => ({ settings: {}, defaults: {} })),
    get("/api/system/check").catch(() => null),
  ]);
  state.system = system;
  state.settings = settings.settings || {};
  state.defaults = settings.defaults || {};
  state.env = env;
  state.ready = true;
  window.__settings = state.settings;
  emit("system");
  return system;
}

export async function refreshStats() {
  try {
    state.stats = await get("/api/stats");
    state.activeJobs = state.stats.activeJobs || 0;
    if (Array.isArray(state.stats.recentJobs)) state.jobs = state.stats.recentJobs;
    emit("stats");
  } catch (err) {
    console.warn("刷新概览失败", err);
  }
}

export async function refreshProjects(params = "") {
  try {
    const data = await get("/api/projects" + (params ? `?${params}` : ""));
    state.projects = data.projects || [];
    emit("projects");
    return state.projects;
  } catch (err) {
    console.warn("刷新项目库失败", err);
    return [];
  }
}

export async function refreshFolders() {
  try {
    const data = await get("/api/folders");
    state.folders = data.folders || [];
    state.trashFolders = data.trashFolders || [];
    state.counts = data.counts || null;
    emit("folders");
    return data;
  } catch (err) {
    console.warn("刷新文件夹失败", err);
    return null;
  }
}

export async function refreshCollections() {
  try {
    const data = await get("/api/collections");
    state.collections = data.collections || [];
    emit("collections");
  } catch (err) {
    console.warn("刷新项目集失败", err);
  }
}

/** 合并 SSE 推送的任务快照 */
export function mergeJobs(payload) {
  if (!payload) return;
  if (Array.isArray(payload.jobs)) {
    // 保留已有任务的日志（SSE 快照不含日志）。
    const byId = new Map(state.jobs.map((j) => [j.id, j]));
    state.jobs = payload.jobs.map((job) => {
      const prev = byId.get(job.id);
      return prev && prev.status === job.status && prev.overall === job.overall ? prev : job;
    });
  }
  state.activeJobs = payload.active || state.jobs.filter((j) => j.status === "running" || j.status === "pending").length;
  emit("jobs");
}

export function findJob(id) {
  return state.jobs.find((j) => j.id === id) || null;
}

export function upsertJob(job) {
  if (!job) return;
  const idx = state.jobs.findIndex((j) => j.id === job.id);
  if (idx >= 0) state.jobs[idx] = { ...state.jobs[idx], ...job };
  else state.jobs.unshift(job);
  emit("jobs");
}

export function engineIsReady() {
  return Boolean(state.env?.ready);
}

// ---- 当前媒体 ----

/** 返回当前媒体项目（可能为 null）。 */
export function activeProject() {
  return state.activeProject || null;
}

/** 设为当前媒体：可传项目对象或项目 ID。 */
export async function setActiveProject(target) {
  let project = target;
  if (typeof target === "string") {
    try {
      project = (await get(`/api/projects/${target}`)).project;
    } catch (_) {
      return null;
    }
  }
  if (!project?.id) return null;
  state.activeProject = project;
  try {
    localStorage.setItem(ACTIVE_KEY, JSON.stringify({
      id: project.id,
      title: project.title,
      mediaId: project.media?.id || "",
      savedAt: Date.now(),
    }));
  } catch (_) { /* 隐私模式忽略 */ }
  emit("active");
  return project;
}

/** 刷新当前媒体的最新数据（任务完成后字幕/成片会变化）。 */
export async function reloadActiveProject() {
  const id = state.activeProject?.id;
  if (!id) return null;
  return setActiveProject(id);
}

/** 启动时从 localStorage 恢复当前媒体；项目已不存在则清除。 */
export async function restoreActiveProject() {
  let saved = null;
  try {
    saved = JSON.parse(localStorage.getItem(ACTIVE_KEY) || "null");
  } catch (_) { saved = null; }
  if (!saved?.id) return null;
  try {
    const data = await get(`/api/projects/${saved.id}`);
    state.activeProject = data.project;
  } catch (_) {
    state.activeProject = null;
    try { localStorage.removeItem(ACTIVE_KEY); } catch (_) { /* 忽略 */ }
    return null;
  }
  emit("active");
  return state.activeProject;
}

/** 清除当前媒体（项目被删除时调用）。 */
export function clearActiveProject() {
  state.activeProject = null;
  try { localStorage.removeItem(ACTIVE_KEY); } catch (_) { /* 忽略 */ }
  emit("active");
}
