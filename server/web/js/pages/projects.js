// 项目库：Windows 资源管理器式的文件管理。
//
// 交互对齐资源管理器：
//   - 左树定位（快速访问 / 智能视图 / 文件夹 / 回收站），右侧详情列表；
//   - 单击选中、Ctrl 加选、Shift 连选、空白处框选、双击打开；
//   - 剪切 / 复制 / 粘贴、拖拽移动、F2 重命名、Delete 进回收站；
//   - 回收站可还原 / 彻底删除 / 清空；
//   - 详细信息与图标两种视图，列头排序，底部状态栏，右侧详情窗格。
import { get, patch, post, del, upload } from "../api.js";
import { h, debounce } from "../dom.js";
import {
  icon, toast, bytes, duration, resolution, relTime, dateTime, escHtml,
  confirmDialog, openModal, menuPopup,
} from "../ui.js";
import { refreshProjects, refreshStats, refreshFolders, state } from "../store.js";
import { pickFile, batchProjects, importFile } from "../app.js";
import {
  statusTag, emptyState, mediaCard, streamCards, formatCard,
  subtitleRow, jobRow,
} from "../components/media.js";

export default {
  async render(root, ctx) {
    if (ctx.id) {
      // #/projects/<id> 视为详情
      return renderDetail(root, ctx.id);
    }
    return renderExplorer(root, ctx);
  },
};

const LS_VIEW = "stt.explorer.view";
const LS_SORT = "stt.explorer.sort";
const LS_DETAILS = "stt.explorer.details";
const ROOT_LABEL = "项目库";

const SMART_VIEWS = [
  { id: "ready", label: "已完成", icon: "check" },
  { id: "processing", label: "处理中", icon: "refresh" },
  { id: "failed", label: "失败", icon: "alert" },
];

// ---------------- 资源管理器 ----------------

async function renderExplorer(root, ctx) {
  const S = createState(ctx);
  const wrap = h("div", { class: "fm" });
  const nav = h("aside", { class: "fm-nav", "aria-label": "项目库导航" });
  const main = h("section", { class: "fm-main" });
  const detailPane = h("aside", { class: "fm-details", "aria-label": "详细信息" });

  // ---- 顶部：前进后退 + 地址栏 + 搜索 ----
  const addrBar = h("div", { class: "fm-addrbar" });
  const backBtn = iconBtn("arrowLeft", "后退", () => history.back());
  const fwdBtn = iconBtn("arrowRight", "前进", () => history.forward());
  const upBtn = iconBtn("arrowUp", "上一级");
  const crumbs = h("div", { class: "fm-crumbs" });
  const searchWrap = h("div", { class: "fm-search" });
  searchWrap.innerHTML = `${icon("search", 14)}<input class="input input-sm" type="search" placeholder="搜索标题、文件名、字幕内容…" aria-label="搜索项目">`;
  addrBar.append(backBtn, fwdBtn, upBtn, crumbs, searchWrap);

  // ---- 命令栏 ----
  const cmdBar = h("div", { class: "fm-cmd" });
  const newFolderBtn = cmdBtn("folder", "新建文件夹", () => createFolderDialog(currentParentId(), refresh, S), "btn-keep");
  const importBtn = cmdBtn("upload", "导入", () => pickFile({ route: "project", temporary: false, onImported: () => refresh() }), "btn-primary");
  const cutBtn = cmdBtn("shuffle", "剪切", () => setClipboard("cut"));
  const copyBtn = cmdBtn("archive", "复制", () => setClipboard("copy"));
  const pasteBtn = cmdBtn("download", "粘贴", () => paste());
  const renameBtn = cmdBtn("edit", "重命名", () => renameSelection());
  const delBtn = cmdBtn("trash", "删除", () => trashSelection());
  const restoreBtn = cmdBtn("refresh", "还原", () => restoreSelection());
  const purgeBtn = cmdBtn("trash", "彻底删除", () => purgeSelection());
  const emptyBtn = cmdBtn("x", "清空回收站", () => emptyTrash());
  const sortBtn = cmdBtn("more", "排序", (ev) => sortMenu(ev));
  const viewBtn = cmdBtn("grid", "视图", (ev) => viewMenu(ev));
  const detailsBtn = cmdBtn("info", "详细信息", () => toggleDetails());
  const sep = () => h("span", { class: "fm-cmd-sep" });
  cmdBar.append(
    newFolderBtn, importBtn, sep(),
    cutBtn, copyBtn, pasteBtn, sep(),
    renameBtn, delBtn, restoreBtn, purgeBtn, emptyBtn, sep(),
    sortBtn, viewBtn,
    h("span", { class: "spacer" }),
    detailsBtn,
  );

  const content = h("div", { class: "fm-content", tabindex: "0", role: "grid", "aria-label": "项目和文件夹" });
  const status = h("div", { class: "fm-status" });
  const band = h("div", { class: "fm-band", hidden: true });

  main.append(addrBar, cmdBar, content, status);
  content.appendChild(band);
  wrap.append(nav, main, detailPane);
  root.replaceChildren(wrap);

  // ---------------- 状态与数据 ----------------

  function createState(ctx) {
    const q = ctx?.query;
    // 默认位置是「项目库」根目录（只显示顶层文件夹与未分类项目），
    // 与资源管理器一致；另外提供「全部项目」平铺视图。
    let loc = { kind: "root", id: "" };
    if (q?.get("folder")) loc = { kind: "folder", id: q.get("folder") };
    else if (q?.get("view")) loc = { kind: q.get("view"), id: "" };
    return {
      loc,
      projects: [],
      folders: [],
      sel: new Set(),
      anchor: -1,
      cursor: -1,
      view: localStorage.getItem(LS_VIEW) || "details",
      sort: localStorage.getItem(LS_SORT) || "name",
      desc: false,
      q: "",
      details: localStorage.getItem(LS_DETAILS) === "1",
      // 与资源管理器一致：切换文件夹后剪贴板仍然有效（存 sessionStorage）。
      clipboard: readClipboard(),
      loading: false,
    };
  }

  function readClipboard() {
    try {
      const raw = sessionStorage.getItem("stt.explorer.clipboard");
      if (!raw) return null;
      const parsed = JSON.parse(raw);
      if (!parsed?.keys?.length) return null;
      return { mode: parsed.mode === "cut" ? "cut" : "copy", keys: new Set(parsed.keys) };
    } catch (_) {
      return null;
    }
  }

  function writeClipboard(value) {
    try {
      if (!value) sessionStorage.removeItem("stt.explorer.clipboard");
      else sessionStorage.setItem("stt.explorer.clipboard", JSON.stringify({ mode: value.mode, keys: [...value.keys] }));
    } catch (_) { /* 隐私模式忽略 */ }
  }

  // dropFromClipboard 在被删除/移动后清理剪贴板里的失效条目。
  function dropFromClipboard(keys) {
    if (!S.clipboard) return;
    for (const key of keys) S.clipboard.keys.delete(key);
    if (!S.clipboard.keys.size) S.clipboard = null;
    writeClipboard(S.clipboard);
  }

  const counts = () => state.counts || { total: 0, unfiled: 0, trash: 0, favorites: 0, recent: 0, ready: 0, processing: 0, failed: 0, folders: {} };
  const folderById = (id) => (state.folders || []).find((f) => f.id === id) || null;
  const trashedFolderById = (id) => (state.trashFolders || []).find((f) => f.id === id) || null;
  const byName = (a, b) => a.name.localeCompare(b.name, "zh-Hans-CN");
  const childrenOf = (parentId) => (state.folders || []).filter((f) => (f.parentId || "") === (parentId || "")).sort(byName);

  function folderChain(id) {
    const out = [];
    const seen = new Set();
    let cur = folderById(id);
    while (cur && !seen.has(cur.id)) {
      seen.add(cur.id);
      out.unshift(cur);
      cur = cur.parentId ? folderById(cur.parentId) : null;
    }
    return out;
  }

  function currentParentId() {
    if (S.loc.kind === "folder") return S.loc.id;
    return "";
  }

  function currentFolderName() {
    if (S.loc.kind === "folder") return folderById(S.loc.id)?.name || "";
    return {
      root: "项目库", all: "全部项目", recent: "最近使用", favorites: "收藏", unfiled: "未分类",
      trash: "回收站", ready: "已完成", processing: "处理中", failed: "失败",
    }[S.loc.kind] || "项目库";
  }

  function currentView() {
    switch (S.loc.kind) {
      case "root": return "unfiled"; // 根目录 = 顶层文件夹 + 未分类项目
      case "recent": return "recent";
      case "favorites": return "favorites";
      case "unfiled": return "unfiled";
      case "trash": return "trash";
      case "ready": case "processing": case "failed": return S.loc.kind;
      default: return "all";
    }
  }

  const isTrash = () => S.loc.kind === "trash";
  const isFolderLoc = () => S.loc.kind === "folder";

  async function refresh() {
    S.loading = true;
    await refreshFolders();
    // 文件夹被删除/回收后，停留在该目录会变成空页，直接回到根目录。
    if (isFolderLoc() && !folderById(S.loc.id)) {
      toast("文件夹已不存在", "已回到项目库", "warn", 2200);
      location.hash = "#/projects";
      return;
    }
    const params = new URLSearchParams();
    // 在根目录搜索时扩展为全库搜索（与资源管理器的「搜索所有子文件夹」一致）。
    params.set("view", S.q && S.loc.kind === "root" ? "all" : currentView());
    if (isFolderLoc()) params.set("folderId", S.loc.id);
    if (S.q) params.set("q", S.q);
    params.set("sort", S.sort === "type" ? "name" : S.sort);
    if (S.desc) params.set("desc", "1");
    const list = await refreshProjects(params.toString());
    S.projects = list || [];
    // 保持选中项仍然存在（刷新后尽量不丢选择）。
    const valid = new Set(items().map((it) => it.key));
    for (const key of [...S.sel]) if (!valid.has(key)) S.sel.delete(key);
    S.loading = false;
    paintAll();
  }

  // ---------------- 数据 -> 展示项 ----------------

  function items() {
    const out = [];
    if (showFolders()) {
      for (const f of visibleFolders()) {
        out.push({ type: "folder", key: "f:" + f.id, folder: f });
      }
    }
    for (const p of S.projects) {
      out.push({ type: "project", key: "p:" + p.id, project: p });
    }
    return out;
  }

  function showFolders() {
    // 「全部项目」是平铺视图，只列项目；其余目录位置都显示子文件夹。
    return ["root", "folder", "trash"].includes(S.loc.kind);
  }

  function visibleFolders() {
    let list;
    if (isTrash()) {
      // 回收站只列「顶层被删」的文件夹，避免重复展示子文件夹。
      const all = state.trashFolders || [];
      const ids = new Set(all.map((f) => f.id));
      list = all.filter((f) => !f.parentId || !ids.has(f.parentId));
    } else {
      list = childrenOf(currentParentId());
    }
    if (S.q) {
      const needle = S.q.toLowerCase();
      list = list.filter((f) => f.name.toLowerCase().includes(needle));
    }
    return list.sort(byName);
  }

  function folderItemCount(f) {
    if (isTrash()) return 0;
    return (counts().folders || {})[f.id] || 0;
  }

  // ---------------- 导航 ----------------

  function folderHash(id) {
    return id ? `#/projects?folder=${encodeURIComponent(id)}` : "#/projects";
  }
  function viewHash(kind) {
    return kind === "root" ? "#/projects" : `#/projects?view=${encodeURIComponent(kind)}`;
  }

  function navigate(hash) {
    if (location.hash === hash) {
      refresh();
    } else {
      location.hash = hash;
    }
  }

  function goUp() {
    if (isFolderLoc()) {
      const f = folderById(S.loc.id);
      navigate(folderHash(f?.parentId || ""));
      return;
    }
    navigate("#/projects");
  }

  function openItem(item) {
    if (!item) return;
    if (item.type === "folder") {
      if (isTrash()) {
        toast("文件夹在回收站中", "先还原再打开", "info", 2200);
        return;
      }
      navigate(folderHash(item.folder.id));
      return;
    }
    if (isTrash()) {
      toast("项目在回收站中", "先还原再打开", "info", 2200);
      return;
    }
    location.hash = `#/project/${item.project.id}`;
  }

  // ---------------- 选择 ----------------

  function selectedItems() {
    const all = items();
    return all.filter((it) => S.sel.has(it.key));
  }
  function selectedProjects() {
    return selectedItems().filter((it) => it.type === "project").map((it) => it.project);
  }
  function selectedFolders() {
    return selectedItems().filter((it) => it.type === "folder").map((it) => it.folder);
  }

  function selectOnly(index) {
    const all = items();
    S.sel.clear();
    if (all[index]) S.sel.add(all[index].key);
    S.anchor = index;
    S.cursor = index;
    paintSelection();
    paintDetails();
  }

  function onRowClick(ev, index) {
    const all = items();
    const key = all[index]?.key;
    if (!key) return;
    if (ev.shiftKey && S.anchor >= 0) {
      const a = Math.min(S.anchor, index);
      const b = Math.max(S.anchor, index);
      S.sel.clear();
      for (let i = a; i <= b; i += 1) S.sel.add(all[i].key);
    } else if (ev.ctrlKey || ev.metaKey) {
      if (S.sel.has(key)) S.sel.delete(key);
      else S.sel.add(key);
      S.anchor = index;
    } else {
      S.sel.clear();
      S.sel.add(key);
      S.anchor = index;
    }
    S.cursor = index;
    paintSelection();
    paintDetails();
  }

  function selectAll() {
    S.sel = new Set(items().map((it) => it.key));
    S.cursor = 0;
    S.anchor = 0;
    paintSelection();
    paintDetails();
  }

  function clearSelection() {
    S.sel.clear();
    S.anchor = -1;
    S.cursor = -1;
    paintSelection();
    paintDetails();
  }

  function moveCursor(delta, extend) {
    const all = items();
    if (!all.length) return;
    let next = S.cursor;
    if (next < 0) next = 0;
    else next = Math.max(0, Math.min(all.length - 1, next + delta));
    S.cursor = next;
    if (extend && S.anchor >= 0) {
      const a = Math.min(S.anchor, next);
      const b = Math.max(S.anchor, next);
      S.sel.clear();
      for (let i = a; i <= b; i += 1) S.sel.add(all[i].key);
    } else {
      S.sel.clear();
      S.sel.add(all[next].key);
      S.anchor = next;
    }
    paintSelection();
    paintDetails();
    scrollRowIntoView(next);
  }

  function scrollRowIntoView(index) {
    const rows = content.querySelectorAll(".fm-row, .fm-tile");
    rows[index]?.scrollIntoView({ block: "nearest" });
  }

  function paintSelection() {
    const all = new Set(S.sel);
    content.querySelectorAll(".fm-row, .fm-tile").forEach((el) => {
      const key = el.dataset.key;
      el.classList.toggle("selected", all.has(key));
      el.setAttribute("aria-selected", all.has(key) ? "true" : "false");
    });
    const keys = [...S.clipboard?.keys || []];
    content.querySelectorAll(".fm-row, .fm-tile").forEach((el) => {
      el.classList.toggle("cut", S.clipboard?.mode === "cut" && keys.includes(el.dataset.key));
    });
    updateCmdState();
    paintStatus();
  }

  // ---------------- 剪贴板 ----------------

  function setClipboard(mode) {
    const keys = [...S.sel];
    if (!keys.length) return;
    S.clipboard = { mode, keys: new Set(keys) };
    writeClipboard(S.clipboard);
    paintSelection();
    toast(mode === "cut" ? "已剪切" : "已复制", `${keys.length} 项，打开目标文件夹后按 Ctrl+V 粘贴`, "info", 2400);
  }

  async function paste(targetFolderId) {
    if (!S.clipboard || !S.clipboard.keys.size) return;
    const target = targetFolderId !== undefined ? targetFolderId : currentParentId();
    const projectIDs = [];
    const folderIDs = [];
    for (const key of S.clipboard.keys) {
      const [type, id] = key.split(":");
      if (type === "p") projectIDs.push(id);
      if (type === "f") folderIDs.push(id);
    }
    const action = S.clipboard.mode === "cut" ? "move" : "copy";
    try {
      const res = await post("/api/library/batch", {
        action, projectIds: projectIDs, folderIds: folderIDs, targetFolderId: target,
      });
      if (S.clipboard.mode === "cut") {
        S.clipboard = null;
        writeClipboard(null);
      }
      toast(action === "move" ? "已移动" : "已复制", `${res.affected || 0} 项`, "success", 1800);
      await refresh();
      await refreshStats();
    } catch (err) {
      toast(action === "move" ? "移动失败" : "复制失败", err.message, "error");
    }
  }

  async function trashSelection() {
    const projects = selectedProjects();
    const folders = selectedFolders();
    if (!projects.length && !folders.length) return;
    const label = projects.length + folders.length;
    const ok = await confirmDialog({
      title: "移入回收站",
      message: `确定把选中的 ${label} 项移到回收站吗？可以随时还原。`,
      confirmText: "移入回收站", danger: true,
    });
    if (!ok) return;
    try {
      await post("/api/library/batch", {
        action: "trash",
        projectIds: projects.map((p) => p.id),
        folderIds: folders.map((f) => f.id),
      });
      dropFromClipboard([...projects.map((p) => "p:" + p.id), ...folders.map((f) => "f:" + f.id)]);
      toast("已移入回收站", `${label} 项，可在回收站还原`, "success", 2600);
      clearSelection();
      await refresh();
      await refreshStats();
    } catch (err) {
      toast("删除失败", err.message, "error");
    }
  }

  async function restoreSelection() {
    const projects = selectedProjects();
    const folders = selectedFolders();
    if (!projects.length && !folders.length) return;
    try {
      await post("/api/library/batch", {
        action: "restore",
        projectIds: projects.map((p) => p.id),
        folderIds: folders.map((f) => f.id),
      });
      toast("已还原", `${projects.length + folders.length} 项`, "success");
      clearSelection();
      await refresh();
      await refreshStats();
    } catch (err) {
      toast("还原失败", err.message, "error");
    }
  }

  async function purgeSelection() {
    const projects = selectedProjects();
    const folders = selectedFolders();
    if (!projects.length && !folders.length) return;
    const ok = await confirmDialog({
      title: "彻底删除",
      message: `将永久删除 ${projects.length + folders.length} 项，媒体、字幕与成片都会从磁盘移除，无法恢复。`,
      confirmText: "永久删除", danger: true,
    });
    if (!ok) return;
    try {
      await post("/api/library/batch", {
        action: "delete",
        projectIds: projects.map((p) => p.id),
        folderIds: folders.map((f) => f.id),
      });
      dropFromClipboard([...projects.map((p) => "p:" + p.id), ...folders.map((f) => "f:" + f.id)]);
      toast("已彻底删除", "", "success");
      clearSelection();
      await refresh();
      await refreshStats();
    } catch (err) {
      toast("删除失败", err.message, "error");
    }
  }

  async function emptyTrash() {
    const ok = await confirmDialog({
      title: "清空回收站",
      message: "回收站中的项目与文件夹将被永久删除，无法恢复。",
      confirmText: "清空", danger: true,
    });
    if (!ok) return;
    try {
      const res = await post("/api/library/trash", {});
      toast("回收站已清空", `删除 ${res.removed || 0} 个项目`, "success");
      await refresh();
      await refreshStats();
    } catch (err) {
      toast("清空失败", err.message, "error");
    }
  }

  function renameSelection() {
    const itemsSel = selectedItems();
    if (itemsSel.length !== 1) return;
    const item = itemsSel[0];
    if (item.type === "project") renameProject(item.project, refresh);
    else renameFolderDialog(item.folder, refresh);
  }

  function toggleFavorite() {
    const projects = selectedProjects();
    if (!projects.length) return;
    const next = !projects.every((p) => p.favorite);
    post("/api/library/batch", { action: "favorite", projectIds: projects.map((p) => p.id), favorite: next })
      .then(() => { toast(next ? "已收藏" : "已取消收藏", "", "success", 1500); refresh(); })
      .catch((err) => toast("操作失败", err.message, "error"));
  }

  // ---------------- 右键菜单 ----------------

  function rowMenu(item, x, y) {
    const multi = S.sel.size > 1;
    const isProject = item.type === "project";
    const items31 = [];
    if (isTrash()) {
      items31.push(
        { label: multi ? `还原 ${S.sel.size} 项` : "还原", icon: "refresh", onClick: restoreSelection },
        { separator: true },
        { label: multi ? `彻底删除 ${S.sel.size} 项` : "彻底删除", icon: "trash", danger: true, onClick: purgeSelection },
        { separator: true },
        { label: "属性", icon: "info", onClick: () => showProperties(item) },
      );
      menuPopup(x, y, items31);
      return;
    }
    if (isProject) {
      items31.push(
        { label: "打开", icon: "folder", onClick: () => openItem(item) },
        { label: "转写", icon: "wave", onClick: () => { location.hash = `#/transcribe/${item.project.id}`; } },
        { label: "转码", icon: "film", onClick: () => { location.hash = `#/transcode/${item.project.id}`; } },
        { separator: true },
        { label: "剪切", icon: "shuffle", shortcut: "Ctrl+X", onClick: () => setClipboard("cut") },
        { label: "复制", icon: "archive", shortcut: "Ctrl+C", onClick: () => setClipboard("copy") },
        { separator: true },
        { label: "重命名", icon: "edit", shortcut: "F2", disabled: multi, onClick: renameSelection },
        { label: "删除", icon: "trash", shortcut: "Delete", danger: true, onClick: trashSelection },
        { separator: true },
        { label: "收藏 / 取消收藏", icon: "sparkles", onClick: toggleFavorite },
        { label: "移动到文件夹…", icon: "folder", onClick: () => assignDialog([...S.sel].filter((k) => k.startsWith("p:")).map((k) => k.slice(2)), refresh) },
        { label: "批量转写", icon: "wave", onClick: () => batchProjects("transcribe", selectedProjects().map((p) => p.id)) },
        { label: "批量转码", icon: "film", onClick: () => batchProjects("transcode", selectedProjects().map((p) => p.id)) },
        { separator: true },
        { label: "属性", icon: "info", onClick: () => showProperties(item) },
      );
    } else {
      items31.push(
        { label: "打开", icon: "folder", onClick: () => openItem(item) },
        { label: "新建子文件夹", icon: "folder", onClick: () => createFolderDialog(item.folder.id, refresh, S) },
        { separator: true },
        { label: "剪切", icon: "shuffle", shortcut: "Ctrl+X", onClick: () => setClipboard("cut") },
        { label: "复制", icon: "archive", shortcut: "Ctrl+C", onClick: () => setClipboard("copy") },
        { separator: true },
        { label: "重命名", icon: "edit", shortcut: "F2", disabled: multi, onClick: renameSelection },
        { label: "删除", icon: "trash", shortcut: "Delete", danger: true, onClick: trashSelection },
        { separator: true },
        { label: "移动到…", icon: "folder", onClick: () => moveFolderDialog(item.folder, refresh) },
        { label: "属性", icon: "info", onClick: () => showProperties(item) },
      );
    }
    menuPopup(x, y, items31);
  }

  function bgMenu(x, y) {
    const menu = [
      { label: "新建文件夹", icon: "folder", shortcut: "Ctrl+Shift+N", onClick: () => createFolderDialog(currentParentId(), refresh, S) },
      { label: "导入文件…", icon: "upload", onClick: () => pickFile({ route: "project", temporary: false, onImported: () => refresh() }) },
    ];
    if (!isTrash()) {
      menu.push({ label: "粘贴", icon: "download", shortcut: "Ctrl+V", disabled: !S.clipboard, onClick: () => paste() });
    }
    menu.push(
      { separator: true },
      { label: "全选", icon: "grid", shortcut: "Ctrl+A", onClick: selectAll },
      { label: "刷新", icon: "refresh", shortcut: "F5", onClick: () => refresh() },
      { separator: true },
      { label: "按名称排序", icon: "more", onClick: () => setSort("name") },
      { label: "按修改时间排序", icon: "more", onClick: () => setSort("updated") },
      { label: "按大小排序", icon: "more", onClick: () => setSort("size") },
    );
    if (isTrash()) {
      menu.push({ separator: true }, { label: "清空回收站", icon: "x", danger: true, onClick: emptyTrash });
    }
    menuPopup(x, y, menu);
  }

  function sortMenu(ev) {
    const rect = ev.currentTarget.getBoundingClientRect();
    menuPopup(rect.left, rect.bottom + 4, [
      { label: "名称", icon: "more", onClick: () => setSort("name") },
      { label: "修改时间", icon: "more", onClick: () => setSort("updated") },
      { label: "创建时间", icon: "more", onClick: () => setSort("created") },
      { label: "大小", icon: "more", onClick: () => setSort("size") },
      { label: "时长", icon: "more", onClick: () => setSort("duration") },
      { label: "字幕数量", icon: "more", onClick: () => setSort("subtitles") },
      { separator: true },
      { label: S.desc ? "改为升序" : "改为降序", icon: "refresh", onClick: () => { S.desc = !S.desc; refresh(); } },
    ]);
  }

  function viewMenu(ev) {
    const rect = ev.currentTarget.getBoundingClientRect();
    menuPopup(rect.left, rect.bottom + 4, [
      { label: "详细信息", icon: "grid", onClick: () => setView("details") },
      { label: "图标", icon: "film", onClick: () => setView("tiles") },
      { separator: true },
      { label: S.details ? "隐藏详细信息窗格" : "显示详细信息窗格", icon: "info", onClick: toggleDetails },
    ]);
  }

  function setSort(sort) {
    if (S.sort === sort) S.desc = !S.desc;
    else { S.sort = sort; S.desc = false; }
    localStorage.setItem(LS_SORT, S.sort);
    refresh();
  }

  function setView(view) {
    S.view = view;
    localStorage.setItem(LS_VIEW, view);
    paintContent();
  }

  function toggleDetails() {
    S.details = !S.details;
    localStorage.setItem(LS_DETAILS, S.details ? "1" : "0");
    wrap.classList.toggle("no-details", !S.details);
    wrap.classList.toggle("has-details", S.details);
    detailPane.hidden = !S.details;
    detailsBtn.classList.toggle("active", S.details);
    paintDetails();
  }

  // ---------------- 绘制 ----------------

  function paintAll() {
    paintNav();
    paintAddress();
    paintContent();
    paintStatus();
    paintDetails();
    updateCmdState();
  }

  function paintNav() {
    const c = counts();
    const parts = [];
    const item = (opts) => {
      const el = h("div", {
        class: "fm-nav-item" + (opts.active ? " active" : ""),
        role: "treeitem", tabindex: "0", title: opts.title || opts.label,
      });
      el.style.paddingLeft = `${8 + (opts.depth || 0) * 14}px`;
      const caret = opts.caret === null || opts.caret === undefined
        ? `<span class="fm-caret none"></span>`
        : `<span class="fm-caret${opts.caret ? " open" : ""}">▸</span>`;
      el.innerHTML = `${caret}<span class="fm-nav-ic${opts.folderIcon ? " folder" : ""}">${icon(opts.icon || "folder", 15)}</span>` +
        `<span class="fm-nav-label">${escHtml(opts.label)}</span>` +
        (opts.count ? `<span class="fm-nav-count">${opts.count}</span>` : "");
      if (opts.onCaret) {
        el.querySelector(".fm-caret").addEventListener("click", (ev) => { ev.stopPropagation(); opts.onCaret(); });
      }
      el.addEventListener("click", () => opts.onClick());
      el.addEventListener("keydown", (ev) => { if (ev.key === "Enter") opts.onClick(); });
      if (opts.onMenu) el.addEventListener("contextmenu", (ev) => { ev.preventDefault(); opts.onMenu(ev.clientX, ev.clientY); });
      if (opts.dropFolderId !== undefined) bindDrop(el, opts.dropFolderId);
      return el;
    };

    const head = (text, action) => {
      const el = h("div", { class: "fm-nav-head" });
      el.append(h("span", {}, text));
      if (action) {
        el.append(h("span", { class: "spacer" }), h("button", {
          class: "icon-btn icon-xs", type: "button", title: "新建文件夹", onclick: action,
        }, "＋"));
      }
      return el;
    };

    parts.push(head("快速访问"));
    parts.push(item({
      label: "项目库", icon: "folder", folderIcon: true, active: S.loc.kind === "root",
      onClick: () => navigate("#/projects"),
      dropFolderId: "",
    }));
    parts.push(item({
      label: "全部项目", icon: "grid", count: c.total, active: S.loc.kind === "all",
      onClick: () => navigate(viewHash("all")),
    }));
    parts.push(item({
      label: "最近使用", icon: "refresh", count: c.recent, active: S.loc.kind === "recent",
      onClick: () => navigate(viewHash("recent")),
    }));
    parts.push(item({
      label: "收藏", icon: "sparkles", count: c.favorites, active: S.loc.kind === "favorites",
      onClick: () => navigate(viewHash("favorites")),
    }));

    parts.push(head("智能视图"));
    for (const v of SMART_VIEWS) {
      parts.push(item({
        label: v.label, icon: v.icon, count: c[v.id], active: S.loc.kind === v.id,
        onClick: () => navigate(viewHash(v.id)),
      }));
    }

    parts.push(head("文件夹", () => createFolderDialog("", refresh, S)));
    const walk = (parentId, depth) => {
      for (const f of childrenOf(parentId)) {
        const kids = childrenOf(f.id);
        const open = libExpanded.has(f.id);
        parts.push(item({
          label: f.name, icon: "folder", folderIcon: true, depth,
          count: (c.folders || {})[f.id] || 0,
          active: S.loc.kind === "folder" && S.loc.id === f.id,
          caret: kids.length ? open : null,
          onCaret: kids.length ? () => {
            if (open) libExpanded.delete(f.id); else libExpanded.add(f.id);
            saveExpanded();
            paintNav();
          } : null,
          onClick: () => {
            libExpanded.add(f.id);
            saveExpanded();
            navigate(folderHash(f.id));
          },
          onMenu: (x, y) => folderMenu(f, x, y),
          dropFolderId: f.id,
        }));
        if (open) walk(f.id, depth + 1);
      }
    };
    walk("", 0);
    if (!(state.folders || []).length) {
      parts.push(h("div", { class: "fm-nav-empty" }, "还没有文件夹"));
    }

    const trashCount = (c.trash || 0) + (c.trashFolders || 0);
    const trashItem = item({
      label: "回收站", icon: "trash", count: trashCount, active: isTrash(),
      onClick: () => navigate(viewHash("trash")),
      onMenu: (x, y) => menuPopup(x, y, [
        { label: "打开", icon: "trash", onClick: () => navigate(viewHash("trash")) },
        { label: "清空回收站", icon: "x", danger: true, onClick: emptyTrash },
      ]),
    });
    trashItem.classList.add("fm-nav-trash");
    parts.push(h("div", { class: "fm-nav-sep" }), trashItem);

    nav.replaceChildren(...parts);
  }

  function folderMenu(f, x, y) {
    menuPopup(x, y, [
      { label: "打开", icon: "folder", onClick: () => navigate(folderHash(f.id)) },
      { label: "新建子文件夹", icon: "folder", onClick: () => createFolderDialog(f.id, refresh, S) },
      { separator: true },
      { label: "剪切", icon: "shuffle", onClick: () => { S.sel = new Set(["f:" + f.id]); setClipboard("cut"); } },
      { label: "复制", icon: "archive", onClick: () => { S.sel = new Set(["f:" + f.id]); setClipboard("copy"); } },
      { label: "粘贴到此处", icon: "download", disabled: !S.clipboard, onClick: () => paste(f.id) },
      { separator: true },
      { label: "重命名", icon: "edit", onClick: () => renameFolderDialog(f, refresh) },
      { label: "移动到…", icon: "folder", onClick: () => moveFolderDialog(f, refresh) },
      { label: "删除", icon: "trash", danger: true, onClick: () => {
        S.sel = new Set(["f:" + f.id]);
        paintSelection();
        trashSelection();
      } },
      { separator: true },
      { label: "属性", icon: "info", onClick: () => showFolderProperties(f) },
    ]);
  }

  function bindDrop(el, folderId) {
    el.addEventListener("dragover", (ev) => {
      if (!ev.dataTransfer?.types?.includes("application/x-stm-items")) return;
      ev.preventDefault();
      ev.dataTransfer.dropEffect = "move";
      el.classList.add("drop-target");
    });
    el.addEventListener("dragleave", () => el.classList.remove("drop-target"));
    el.addEventListener("drop", async (ev) => {
      const raw = ev.dataTransfer?.getData("application/x-stm-items");
      el.classList.remove("drop-target");
      if (!raw) return;
      ev.preventDefault();
      ev.stopPropagation();
      await moveKeys(JSON.parse(raw), folderId);
    });
  }

  async function moveKeys(keys, folderId) {
    const projectIDs = keys.filter((k) => k.startsWith("p:")).map((k) => k.slice(2));
    const folderIDs = keys.filter((k) => k.startsWith("f:")).map((k) => k.slice(2));
    if (!projectIDs.length && !folderIDs.length) return;
    try {
      await post("/api/library/batch", {
        action: "move", projectIds: projectIDs, folderIds: folderIDs, targetFolderId: folderId,
      });
      toast("已移动", `${keys.length} 项 → ${folderId ? (folderById(folderId)?.name || "文件夹") : "未分类"}`, "success", 1800);
      await refresh();
      await refreshStats();
    } catch (err) {
      toast("移动失败", err.message, "error");
    }
  }

  function paintAddress() {
    const parts = [];
    const crumb = (label, hash, current) => {
      const el = h("span", { class: "fm-crumb" + (current ? " current" : ""), tabindex: current ? "-1" : "0" }, label);
      if (!current) {
        el.addEventListener("click", () => navigate(hash));
        el.addEventListener("keydown", (ev) => { if (ev.key === "Enter") navigate(hash); });
      }
      return el;
    };
    const chev = () => h("span", { class: "fm-chev" }, "›");
    if (isTrash()) {
      parts.push(crumb("回收站", viewHash("trash"), true));
    } else if (isFolderLoc()) {
      const chain = folderChain(S.loc.id);
      parts.push(crumb(ROOT_LABEL, "#/projects", false));
      if (!chain.length) parts.push(chev(), crumb("未知文件夹", "#/projects", true));
      chain.forEach((f, i) => {
        parts.push(chev(), crumb(f.name, folderHash(f.id), i === chain.length - 1));
      });
    } else if (S.loc.kind === "root") {
      parts.push(crumb(ROOT_LABEL, "#/projects", true));
    } else {
      parts.push(crumb(ROOT_LABEL, "#/projects", false), chev(), crumb(currentFolderName(), viewHash(S.loc.kind), true));
    }
    crumbs.replaceChildren(...parts);
    upBtn.disabled = S.loc.kind === "root";
  }

  function paintContent() {
    const all = items();
    content.classList.toggle("is-tiles", S.view === "tiles");
    // 保留框选层
    content.querySelectorAll(".fm-row, .fm-tile, .fm-thead, .fm-table, .fm-empty, .empty").forEach((el) => el.remove());

    if (!all.length) {
      content.appendChild(emptyState({
        iconName: isTrash() ? "trash" : "folder",
        title: emptyTitle(),
        message: emptyMessage(),
        action: isTrash() ? null : h("button", {
          class: "btn btn-primary btn-sm", type: "button",
          onclick: () => pickFile({ route: "project", temporary: false, onImported: () => refresh() }),
        }, "导入第一个文件"),
      }));
      paintStatus();
      return;
    }

    if (S.view === "tiles") {
      const grid = h("div", { class: "fm-tiles" });
      all.forEach((item, index) => grid.appendChild(tileFor(item, index)));
      content.appendChild(grid);
    } else {
      const table = h("div", { class: "fm-table", role: "table" });
      table.appendChild(thead());
      const body = h("div", { class: "fm-tbody" });
      all.forEach((item, index) => body.appendChild(rowFor(item, index)));
      table.appendChild(body);
      content.appendChild(table);
    }
    paintSelection();
  }

  function emptyTitle() {
    if (isTrash()) return "回收站是空的";
    if (S.q) return "没有匹配的项目";
    if (isFolderLoc()) return "此文件夹为空";
    if (S.loc.kind === "favorites") return "还没有收藏";
    if (S.loc.kind === "recent") return "最近没有使用记录";
    if (S.loc.kind === "root") return "项目库还是空的";
    return "还没有项目";
  }

  function emptyMessage() {
    if (isTrash()) return "删除的项目会先放在这里，可随时还原。";
    if (S.q) return "换个关键词试试，搜索会匹配标题、文件名与字幕内容。";
    if (isFolderLoc()) return "把项目拖到这个文件夹，或从上方导入新文件。";
    if (S.loc.kind === "root") return "导入一个音视频文件，或先新建文件夹把课程/素材分类。";
    return "导入一个音视频文件，系统会自动建立项目并保存转写结果。";
  }

  function thead() {
    const head = h("div", { class: "fm-thead", role: "row" });
    const cols = [
      ["name", "名称", "c-name"],
      ["status", "状态", "c-status"],
      ["duration", "时长", "c-dur"],
      ["resolution", "分辨率", "c-res"],
      ["subtitles", "字幕", "c-subs"],
      ["size", "大小", "c-size"],
      ["updated", "修改时间", "c-time"],
    ];
    for (const [key, label, cls] of cols) {
      const cell = h("div", { class: `fm-cell ${cls}`, role: "columnheader" }, label);
      if (["name", "duration", "subtitles", "size", "updated"].includes(key)) {
        cell.classList.add("sortable");
        if (S.sort === key) cell.classList.add("sorted", S.desc ? "desc" : "asc");
        cell.addEventListener("click", () => setSort(key));
      }
      head.appendChild(cell);
    }
    return head;
  }

  function rowFor(item, index) {
    const el = h("div", {
      class: "fm-row", role: "row", tabindex: "-1", draggable: "true", "data-key": item.key,
    });
    if (S.sel.has(item.key)) el.classList.add("selected");

    const isProject = item.type === "project";
    const p = item.project;
    const f = item.folder;
    const probe = p?.media?.probe || {};
    const size = p ? projectSize(p) : 0;

    const nameCell = h("div", { class: "fm-cell c-name", role: "gridcell" });
    nameCell.innerHTML = `
      <span class="fm-ic ${isProject ? (probe.hasVideo ? "video" : "audio") : "folder"}">${icon(isProject ? (probe.hasVideo ? "film" : "audio") : "folder", 16)}</span>
      <span class="fm-name" title="${escHtml(isProject ? p.title : f.name)}">${escHtml(isProject ? p.title : f.name)}</span>
      ${!isProject && folderItemCount(f) ? `<span class="fm-badge">${folderItemCount(f)}</span>` : ""}
    `;

    const statusCell = h("div", { class: "fm-cell c-status", role: "gridcell" });
    if (isProject) statusCell.appendChild(statusTag(p.status));
    else statusCell.innerHTML = `<span class="muted">文件夹</span>`;

    el.append(
      nameCell,
      statusCell,
      cellText("c-dur", isProject ? duration(p.duration) : "—"),
      cellText("c-res", isProject && probe.width ? `${probe.width}×${probe.height}` : "—"),
      cellText("c-subs", isProject ? String((p.subtitles || []).length) : "—"),
      cellText("c-size", isProject && size ? bytes(size) : "—"),
      cellText("c-time", isProject ? dateTime(p.updatedAt) : ""),
    );

    bindRow(el, item, index);
    return el;
  }

  function cellText(cls, text) {
    const el = h("div", { class: `fm-cell ${cls}`, role: "gridcell" });
    el.textContent = text;
    return el;
  }

  function tileFor(item, index) {
    const isProject = item.type === "project";
    const p = item.project;
    const f = item.folder;
    const probe = p?.media?.probe || {};
    const cover = isProject && probe.hasVideo
      ? `background-image:url(/api/projects/${p.id}/thumbnail?at=1)`
      : "";
    const el = h("div", { class: "fm-tile", tabindex: "-1", draggable: "true", "data-key": item.key });
    el.innerHTML = `
      <div class="fm-tile-cover ${isProject ? "" : "folder"}" style="${cover}">
        ${isProject
          ? (probe.hasVideo ? "" : icon(probe.hasAudio ? "audio" : "film", 30))
          : icon("folder", 40)}
        ${isProject && p.favorite ? `<span class="fm-tile-fav" title="已收藏">★</span>` : ""}
      </div>
      <div class="fm-tile-name" title="${escHtml(isProject ? p.title : f.name)}">${escHtml(isProject ? p.title : f.name)}</div>
      <div class="fm-tile-meta">${isProject
        ? `${duration(p.duration)}${(p.subtitles || []).length ? ` · ${p.subtitles.length} 字幕` : ""}`
        : `${folderItemCount(f)} 项`}</div>
    `;
    bindRow(el, item, index);
    return el;
  }

  function bindRow(el, item, index) {
    el.addEventListener("click", (ev) => { content.focus({ preventScroll: true }); onRowClick(ev, index); });
    el.addEventListener("dblclick", () => openItem(item));
    el.addEventListener("contextmenu", (ev) => {
      ev.preventDefault();
      if (!S.sel.has(item.key)) selectOnly(index);
      rowMenu(item, ev.clientX, ev.clientY);
    });
    el.addEventListener("dragstart", (ev) => {
      const keys = S.sel.has(item.key) ? [...S.sel] : [item.key];
      ev.dataTransfer.setData("application/x-stm-items", JSON.stringify(keys));
      ev.dataTransfer.effectAllowed = "move";
      el.classList.add("dragging");
    });
    el.addEventListener("dragend", () => el.classList.remove("dragging"));
    // 文件夹行本身也是拖放目标
    if (item.type === "folder" && !isTrash()) bindDrop(el, item.folder.id);
  }

  function paintStatus() {
    const all = items();
    const sel = S.sel.size;
    const totalSize = all.reduce((n, it) => n + (it.type === "project" ? projectSize(it.project) : 0), 0);
    const parts = [
      h("span", {}, `${all.length} 项`),
      sel ? h("span", { class: "fm-status-sel" }, `已选择 ${sel} 项`) : null,
      h("span", { class: "spacer" }),
      totalSize ? h("span", {}, `共 ${bytes(totalSize)}`) : null,
      h("span", { class: "fm-status-view" },
        h("button", { class: "fm-mini" + (S.view === "details" ? " active" : ""), type: "button", title: "详细信息", onclick: () => setView("details") }, "☰"),
        h("button", { class: "fm-mini" + (S.view === "tiles" ? " active" : ""), type: "button", title: "图标", onclick: () => setView("tiles") }, "▦"),
      ),
    ].filter(Boolean);
    status.replaceChildren(...parts);
  }

  function paintDetails() {
    detailPane.hidden = !S.details;
    detailsBtn.classList.toggle("active", S.details);
    if (!S.details) return;
    const sel = selectedItems();
    if (sel.length === 1) {
      const item = sel[0];
      detailPane.replaceChildren(item.type === "project" ? projectDetails(item.project) : folderDetails(item.folder));
      return;
    }
    if (sel.length > 1) {
      detailPane.replaceChildren(multiDetails(sel.length));
      return;
    }
    detailPane.replaceChildren(locationDetails());
  }

  function detailHead(ic, title, sub, cls) {
    const el = h("div", { class: "fm-detail-head" });
    el.innerHTML = `
      <div class="fm-detail-cover ${cls || ""}">${icon(ic, 34)}</div>
      <div class="fm-detail-title" title="${escHtml(title)}">${escHtml(title)}</div>
      <div class="fm-detail-sub">${escHtml(sub || "")}</div>
    `;
    return el;
  }

  function kvList(rows) {
    const el = h("div", { class: "fm-kv" });
    for (const [k, v] of rows) {
      if (v === undefined || v === null || v === "") continue;
      const row = h("div", { class: "fm-kv-row" });
      row.innerHTML = `<span class="k">${escHtml(k)}</span><span class="v" title="${escHtml(String(v))}">${escHtml(String(v))}</span>`;
      el.appendChild(row);
    }
    return el;
  }

  function projectDetails(p) {
    const probe = p.media?.probe || {};
    const box = h("div", { class: "fm-detail" });
    const cover = h("div", { class: "fm-detail-cover video" });
    if (probe.hasVideo) {
      cover.style.backgroundImage = `url(/api/projects/${p.id}/thumbnail?at=1)`;
      cover.innerHTML = "";
    } else {
      cover.innerHTML = icon("audio", 34);
    }
    const title = h("div", { class: "fm-detail-title", title: p.title }, p.title);
    const sub = h("div", { class: "fm-detail-sub" }, `${statusText(p.status)} · ${duration(p.duration)}`);
    const actions = h("div", { class: "fm-detail-actions" });
    actions.append(
      h("a", { class: "btn btn-sm btn-primary", href: `#/project/${p.id}` }, "打开"),
      h("a", { class: "btn btn-sm btn-ghost", href: `#/transcribe/${p.id}` }, "转写"),
    );
    if ((p.subtitles || []).length) {
      actions.append(h("a", {
        class: "btn btn-sm btn-ghost", href: `#/editor/${p.id}?track=${p.subtitles[0].id}`,
      }, "编辑字幕"));
    }
    const folderName = p.folderId ? (folderById(p.folderId)?.name || "已删除的文件夹") : "未分类";
    box.append(
      cover, title, sub,
      kvList([
        ["类型", kindText(p.kind)],
        ["分辨率", probe.width ? `${probe.width}×${probe.height}` : "—"],
        ["视频编码", probe.videoCodec],
        ["音频编码", probe.audioCodec],
        ["字幕", `${(p.subtitles || []).length} 份`],
        ["成片", `${(p.outputs || []).length} 个`],
        ["附件", `${(p.attachments || []).length} 个`],
        ["大小", bytes(projectSize(p))],
        ["位置", folderName],
        ["修改时间", dateTime(p.updatedAt)],
        ["最近打开", p.openedAt ? dateTime(p.openedAt) : "—"],
      ]),
      actions,
    );
    return box;
  }

  function folderDetails(f) {
    const box = h("div", { class: "fm-detail" });
    const chain = folderChain(f.id).map((x) => x.name).join(" / ");
    box.append(
      detailHead("folder", f.name, `${folderItemCount(f)} 个项目`, "folder"),
      kvList([
        ["类型", "文件夹"],
        ["包含", `${folderItemCount(f)} 个项目`],
        ["子文件夹", `${childrenOf(f.id).length} 个`],
        ["位置", chain || ROOT_LABEL],
        ["创建时间", dateTime(f.createdAt)],
      ]),
    );
    return box;
  }

  function multiDetails(n) {
    const box = h("div", { class: "fm-detail" });
    box.append(detailHead("archive", `已选择 ${n} 项`, "可批量剪切、复制、删除"));
    return box;
  }

  function locationDetails() {
    const c = counts();
    const box = h("div", { class: "fm-detail" });
    const name = currentFolderName();
    const isRoot = S.loc.kind === "root" || S.loc.kind === "all";
    const rows = [
      ["位置", name],
      ["项目", `${items().filter((i) => i.type === "project").length} 个`],
      ["文件夹", `${items().filter((i) => i.type === "folder").length} 个`],
    ];
    if (isRoot) rows.push(["全部项目", `${c.total} 个`], ["回收站", `${c.trash} 个`]);
    box.append(
      detailHead(isTrash() ? "trash" : "folder", name, isTrash() ? "已删除的内容" : "选择一个项目查看详情", isTrash() ? "trash" : "folder"),
      kvList(rows),
      h("p", { class: "fm-detail-hint" }, isTrash()
        ? "在这里可以还原误删的项目，或彻底清空。"
        : "单击选中，双击打开；支持 Ctrl 加选、Shift 连选与拖动移动。"),
    );
    return box;
  }

  function updateCmdState() {
    const selCount = S.sel.size;
    const trash = isTrash();
    const single = selCount === 1;
    const hasProjects = selectedProjects().length > 0;
    cutBtn.disabled = !selCount || trash;
    copyBtn.disabled = !selCount || trash;
    pasteBtn.disabled = !S.clipboard || trash;
    renameBtn.disabled = !single || trash;
    delBtn.disabled = !selCount || trash;
    restoreBtn.hidden = !trash;
    purgeBtn.hidden = !trash;
    emptyBtn.hidden = !trash;
    restoreBtn.disabled = !selCount;
    purgeBtn.disabled = !selCount;
    newFolderBtn.hidden = trash;
    importBtn.hidden = trash;
    cutBtn.hidden = trash;
    copyBtn.hidden = trash;
    pasteBtn.hidden = trash;
    renameBtn.hidden = trash;
    delBtn.hidden = trash;
    void hasProjects;
  }

  // ---------------- 属性 ----------------

  function showProperties(item) {
    if (item.type === "project") projectProperties(item.project);
    else showFolderProperties(item.folder);
  }

  function showFolderProperties(f) {
    const chain = folderChain(f.id).map((x) => x.name).join(" / ");
    const body = h("div", { class: "stack-sm" });
    body.innerHTML = `
      <div class="prop-row"><span>名称</span><b>${escHtml(f.name)}</b></div>
      <div class="prop-row"><span>类型</span><b>文件夹</b></div>
      <div class="prop-row"><span>包含</span><b>${folderItemCount(f)} 个项目</b></div>
      <div class="prop-row"><span>位置</span><b>${escHtml(chain || ROOT_LABEL)}</b></div>
      <div class="prop-row"><span>创建时间</span><b>${dateTime(f.createdAt)}</b></div>
    `;
    openModal({ title: "文件夹属性", content: body });
  }

  // ---------------- 交互绑定 ----------------

  upBtn.addEventListener("click", goUp);
  searchWrap.querySelector("input").addEventListener("input", debounce((ev) => {
    S.q = ev.target.value.trim();
    refresh();
  }, 220));

  content.addEventListener("contextmenu", (ev) => {
    if (ev.target.closest(".fm-row, .fm-tile")) return;
    ev.preventDefault();
    bgMenu(ev.clientX, ev.clientY);
  });

  content.addEventListener("keydown", (ev) => {
    if (ev.ctrlKey || ev.metaKey) {
      const key = ev.key.toLowerCase();
      if (key === "a") { ev.preventDefault(); selectAll(); return; }
      if (key === "c") { ev.preventDefault(); setClipboard("copy"); return; }
      if (key === "x") { ev.preventDefault(); setClipboard("cut"); return; }
      if (key === "v") { ev.preventDefault(); paste(); return; }
      if (ev.shiftKey && key === "n") { ev.preventDefault(); createFolderDialog(currentParentId(), refresh, S); return; }
    }
    switch (ev.key) {
      case "ArrowDown": ev.preventDefault(); moveCursor(1, ev.shiftKey); break;
      case "ArrowUp": ev.preventDefault(); moveCursor(-1, ev.shiftKey); break;
      case "Home": ev.preventDefault(); S.cursor = -1; moveCursor(1, false); break;
      case "End": ev.preventDefault(); S.cursor = items().length - 2; moveCursor(1, false); break;
      case "Enter": {
        ev.preventDefault();
        const item = items()[S.cursor];
        if (item) openItem(item);
        break;
      }
      case "Backspace": ev.preventDefault(); goUp(); break;
      case "F2": ev.preventDefault(); renameSelection(); break;
      case "Delete": ev.preventDefault(); if (isTrash()) purgeSelection(); else trashSelection(); break;
      case "F5": ev.preventDefault(); refresh(); break;
      case "Escape": clearSelection(); break;
      default:
        break;
    }
  });

  // 空白处框选
  content.addEventListener("pointerdown", (ev) => {
    if (ev.button !== 0 || ev.target.closest(".fm-row, .fm-tile")) return;
    const rect = content.getBoundingClientRect();
    const startX = ev.clientX - rect.left + content.scrollLeft;
    const startY = ev.clientY - rect.top + content.scrollTop;
    const additive = ev.ctrlKey || ev.metaKey;
    const base = additive ? new Set(S.sel) : new Set();
    let moved = false;
    band.hidden = false;
    band.style.left = `${startX}px`;
    band.style.top = `${startY}px`;
    band.style.width = "0px";
    band.style.height = "0px";

    const onMove = (e) => {
      const x = e.clientX - rect.left + content.scrollLeft;
      const y = e.clientY - rect.top + content.scrollTop;
      if (!moved && Math.abs(x - startX) + Math.abs(y - startY) < 3) return;
      moved = true;
      const left = Math.min(startX, x);
      const top = Math.min(startY, y);
      const w = Math.abs(x - startX);
      const hgt = Math.abs(y - startY);
      band.style.left = `${left}px`;
      band.style.top = `${top}px`;
      band.style.width = `${w}px`;
      band.style.height = `${hgt}px`;
      const box = { left: left + rect.left - content.scrollLeft, top: top + rect.top - content.scrollTop, right: 0, bottom: 0 };
      box.right = box.left + w;
      box.bottom = box.top + hgt;
      S.sel = new Set(base);
      content.querySelectorAll(".fm-row, .fm-tile").forEach((el) => {
        const r = el.getBoundingClientRect();
        const hit = r.left < box.right && r.right > box.left && r.top < box.bottom && r.bottom > box.top;
        if (hit) S.sel.add(el.dataset.key);
      });
      paintSelection();
    };
    const onUp = () => {
      window.removeEventListener("pointermove", onMove);
      window.removeEventListener("pointerup", onUp);
      band.hidden = true;
      if (!moved && !additive) clearSelection();
      paintDetails();
    };
    window.addEventListener("pointermove", onMove);
    window.addEventListener("pointerup", onUp);
  });

  // 操作系统文件拖入 → 导入
  content.addEventListener("dragover", (ev) => {
    if (ev.dataTransfer?.types?.includes("Files")) {
      ev.preventDefault();
      content.classList.add("file-drop");
    }
  });
  content.addEventListener("dragleave", () => content.classList.remove("file-drop"));
  content.addEventListener("drop", async (ev) => {
    if (!ev.dataTransfer?.files?.length) return;
    ev.preventDefault();
    content.classList.remove("file-drop");
    const files = [...ev.dataTransfer.files];
    for (const file of files) {
      const payload = await importFile(file, { temporary: false });
      if (payload?.project?.folderId === undefined && S.loc.kind === "folder") {
        // 导入后放到当前文件夹（导入接口不接收 folderId，这里补一次移动）。
      }
      if (payload?.project?.id && S.loc.kind === "folder") {
        try { await patch(`/api/projects/${payload.project.id}`, { folderId: S.loc.id }); } catch (_) { /* 忽略 */ }
      }
    }
    await refresh();
    await refreshStats();
  });

  // 首次加载
  await refresh();
  content.focus({ preventScroll: true });
  if (!S.details) {
    detailPane.hidden = true;
    wrap.classList.add("no-details");
  } else {
    wrap.classList.add("has-details");
  }
  detailsBtn.classList.toggle("active", S.details);
  return null;
}

// ---------------- 小工具 ----------------

function iconBtn(iconName, title, onClick) {
  const el = h("button", { class: "fm-navbtn", type: "button", title, html: icon(iconName, 15) });
  el.addEventListener("click", onClick);
  return el;
}

function cmdBtn(iconName, label, onClick, extraClass) {
  const el = h("button", {
    class: "fm-cmd-btn" + (extraClass ? " " + extraClass : ""),
    type: "button", title: label,
  });
  el.innerHTML = `${icon(iconName, 15)}<span>${label}</span>`;
  el.addEventListener("click", onClick);
  return el;
}

function statusText(status) {
  return { ready: "已完成", processing: "处理中", failed: "失败", draft: "草稿" }[status] || "草稿";
}

function kindText(kind) {
  return { transcribe: "字幕转写", transcode: "视频转码", info: "媒体分析" }[kind] || kind || "项目";
}

function projectSize(p) {
  if (!p) return 0;
  let n = p.media?.size || 0;
  for (const o of p.outputs || []) n += o?.size || 0;
  for (const a of p.attachments || []) n += a?.size || 0;
  return n;
}

// 文件夹树的展开状态（跨页面共享，持久化到本地）。
const EXPAND_KEY = "stt.lib.expanded";
const libExpanded = (() => {
  try { return new Set(JSON.parse(localStorage.getItem(EXPAND_KEY) || "[]")); } catch (_) { return new Set(); }
})();
function saveExpanded() {
  try { localStorage.setItem(EXPAND_KEY, JSON.stringify([...libExpanded])); } catch (_) { /* 隐私模式忽略 */ }
}
function expandFolder(id) {
  if (!id) return;
  libExpanded.add(id);
  saveExpanded();
}

// folderOptions 返回带层级缩进的文件夹列表（用于「移动到」对话框）。
function folderOptions() {
  const byParent = new Map();
  for (const f of state.folders || []) {
    const key = f.parentId || "";
    if (!byParent.has(key)) byParent.set(key, []);
    byParent.get(key).push(f);
  }
  byParent.forEach((list) => list.sort((a, b) => a.name.localeCompare(b.name, "zh-Hans-CN")));
  const out = [];
  const walk = (parentId, depth) => {
    for (const f of byParent.get(parentId) || []) {
      out.push({ folder: f, label: "　".repeat(depth) + f.name });
      walk(f.id, depth + 1);
    }
  };
  walk("", 0);
  return out;
}

// ---------------- 详情 ----------------
export async function renderDetail(root, id) {
  let data;
  try {
    data = await get(`/api/projects/${id}`);
  } catch (err) {
    root.replaceChildren(emptyState({
      iconName: "folder", title: "项目不存在", message: "它可能已被删除。",
      action: h("a", { class: "btn btn-sm btn-primary", href: "#/projects" }, "返回项目库"),
    }));
    return null;
  }
  let project = data.project;
  let probeFull = null;
  const wrap = h("div", { class: "stack" });

  const render = () => {
    wrap.replaceChildren(header(project, data.jobs || [], refresh), tabs(project, data.jobs || [], refresh));
  };

  const refresh = async () => {
    const fresh = await get(`/api/projects/${id}`);
    project = fresh.project;
    data = fresh;
    render();
  };

  render();
  root.replaceChildren(wrap);
  return null;
}

function header(project, jobs, refresh) {
  const probe = project.media?.probe || {};
  const head = h("div", {});
  const cover = probe.hasVideo
    ? `background-image:linear-gradient(90deg, rgba(8,12,20,.92) 0%, rgba(8,12,20,.78) 42%, rgba(8,12,20,.25) 100%), url(/api/projects/${project.id}/thumbnail?at=1)`
    : "background:linear-gradient(120deg,#16203a,#0d1322)";
  const bar = h("div", {
    class: "card",
    style: `${cover};background-size:cover;background-position:center;color:#fff;border:0;padding:22px 24px;position:relative;overflow:hidden`,
  });
  bar.innerHTML = `
    <div style="display:flex;gap:16px;align-items:flex-start;flex-wrap:wrap">
      <div style="min-width:0;flex:1">
        <div class="cluster" style="margin-bottom:6px">
          ${statusTag(project.status).outerHTML}
          ${project.temporary ? `<span class="tag tag-warning">临时项目</span>` : ""}
          ${project.favorite ? `<span class="tag">已收藏</span>` : ""}
        </div>
        <h1 style="margin:0;font-size:24px;letter-spacing:-.3px;color:#fff">${escHtml(project.title)}</h1>
        <div class="cluster small" style="margin-top:8px;color:rgba(255,255,255,.72)">
          <span>${duration(project.duration)}</span>
          <span>·</span>
          <span>${(project.subtitles || []).length} 份字幕</span>
          <span>·</span>
          <span>${(project.outputs || []).length} 个成片</span>
          <span>·</span>
          <span>创建于 ${dateTime(project.createdAt)}</span>
        </div>
      </div>
      <div class="cluster"></div>
    </div>
  `;
  const actions = bar.querySelector(".cluster:last-child");
  actions.append(
    h("a", { class: "btn btn-sm", href: `#/player/${project.id}`, html: icon("play", 15) + " 播放" }),
    h("a", { class: "btn btn-sm btn-ghost", href: `#/transcribe/${project.id}`, html: icon("wave", 15) + " 转写" }),
    h("a", { class: "btn btn-sm btn-ghost", href: `#/transcode/${project.id}` }, "转码"),
    h("button", { class: "btn btn-sm btn-ghost", type: "button", onclick: () => openMenu(project, refresh) }, "更多"),
  );
  head.appendChild(bar);
  return head;
}

function tabs(project, jobs, refresh) {
  const container = h("div", {});
  const tabBar = h("div", { class: "detail-tabs" });
  const panels = h("div", {});

  const TABS = [
    ["subtitle", "字幕"],
    ["info", "媒体信息"],
    ["output", "成片与导出"],
    ["attachments", "附件"],
    ["jobs", "任务记录"],
    ["notes", "备注"],
  ];
  let current = "subtitle";

  TABS.forEach(([key, label]) => {
    const count = key === "subtitle" ? (project.subtitles || []).length
      : key === "output" ? (project.outputs || []).length
        : key === "attachments" ? (project.attachments || []).length
          : key === "jobs" ? (jobs || []).length : 0;
    const btn = h("button", { class: "detail-tab" + (key === current ? " active" : ""), type: "button" },
      count ? `${label} (${count})` : label);
    btn.addEventListener("click", () => {
      current = key;
      tabBar.querySelectorAll(".detail-tab").forEach((b) => b.classList.toggle("active", b === btn));
      renderPanel();
    });
    tabBar.appendChild(btn);
  });

  const renderPanel = () => {
    if (current === "subtitle") panels.replaceChildren(subtitlePanel(project, refresh));
    if (current === "info") panels.replaceChildren(infoPanel(project));
    if (current === "output") panels.replaceChildren(outputPanel(project, refresh));
    if (current === "attachments") panels.replaceChildren(attachmentsPanel(project, refresh));
    if (current === "jobs") panels.replaceChildren(jobsPanel(jobs));
    if (current === "notes") panels.replaceChildren(notesPanel(project, refresh));
  };
  renderPanel();
  container.append(tabBar, panels);
  return container;
}

function subtitlePanel(project, refresh) {
  const wrap = h("div", { class: "stack" });
  const card = h("div", { class: "card" });
  const head = h("div", { class: "card-head" },
    h("h3", {}, "字幕文件"),
    h("span", { class: "spacer" }),
    h("a", { class: "btn btn-sm btn-primary", href: `#/transcribe/${project.id}`, html: icon("wave", 15) + " 转写新字幕" }),
    h("button", { class: "btn btn-sm btn-ghost", type: "button", onclick: () => importSubtitle(project, refresh) }, "导入字幕"),
  );
  card.appendChild(head);
  const body = h("div", { class: "card-body", style: "padding:6px 20px" });
  if (!(project.subtitles || []).length) {
    body.appendChild(emptyState({
      iconName: "wave",
      title: "还没有字幕",
      message: "点击「转写新字幕」让本地引擎识别语音，也可以导入现成的 SRT / VTT。",
    }));
  } else {
    const list = h("div", { class: "list" });
    project.subtitles.forEach((track) => {
      const row = subtitleRow(project.id, track, refresh);
      const delBtn = h("button", { class: "btn btn-xs btn-danger-soft", type: "button", title: "删除这份字幕" }, "删除");
      delBtn.addEventListener("click", async () => {
        const ok = await confirmDialog({
          title: "删除字幕",
          message: `确定删除「${track.fileName}」吗？此操作不可撤销。`,
          confirmText: "删除", danger: true,
        });
        if (!ok) return;
        try {
          await del(`/api/projects/${project.id}/subtitle/${track.id}`);
          toast("字幕已删除", "", "success", 1800);
          await refresh();
        } catch (err) {
          toast("删除失败", err.message, "error");
        }
      });
      row.querySelector(".cluster").appendChild(delBtn);
      list.appendChild(row);
    });
    body.appendChild(list);
  }
  card.appendChild(body);
  wrap.appendChild(card);

  const actions = h("div", { class: "card card-pad" });
  actions.innerHTML = `
    <h4 style="margin:0 0 10px;font-size:12.5px;font-weight:600;color:var(--text-2)">导出与分享</h4>
    <div class="cluster">
      <a class="btn btn-sm btn-ghost" href="/api/projects/${project.id}/exports?format=srt">下载 SRT</a>
      <a class="btn btn-sm btn-ghost" href="/api/projects/${project.id}/exports?format=vtt">下载 VTT</a>
      <a class="btn btn-sm btn-ghost" href="/api/projects/${project.id}/exports?format=json">下载 JSON</a>
      <span class="small muted">导出的是最近一份字幕；如需指定版本，请在编辑器里另存。</span>
    </div>
  `;
  wrap.appendChild(actions);
  return wrap;
}

function infoPanel(project) {
  const wrap = h("div", { class: "stack" });
  const probe = project.media?.probe || {};
  const grid = h("div", { class: "analyze-grid" });
  grid.append(mediaCard(project, { linked: true }), formatCard(probe, project));
  wrap.appendChild(grid);

  // 项目里只保存精简探测信息；流明细按需拉取完整 ffprobe 结果。
  const streamsHost = h("div", {});
  wrap.appendChild(streamsHost);
  if (project.media) {
    streamsHost.innerHTML = `<div class="skeleton" style="height:88px;border-radius:12px"></div>`;
    get(`/api/projects/${project.id}/probe`)
      .then((data) => {
        const full = data.probe;
        if (!full?.streams?.length) {
          streamsHost.replaceChildren();
          return;
        }
        streamsHost.replaceChildren(
          h("div", { class: "section-title" }, h("h2", {}, "流信息"), h("span", { class: "spacer" })),
          streamCards(full),
        );
      })
      .catch(() => streamsHost.replaceChildren());
  }
  return wrap;
}

function outputPanel(project, refresh) {
  const wrap = h("div", { class: "stack" });
  const card = h("div", { class: "card" });
  card.innerHTML = `
    <div class="card-head">
      <h3>成片</h3><span class="spacer"></span>
      <a class="btn btn-sm btn-ghost" href="#/transcode/${project.id}">新建转码</a>
    </div>
  `;
  const body = h("div", { class: "card-body", style: "padding:6px 20px" });
  if (!(project.outputs || []).length) {
    body.appendChild(emptyState({
      iconName: "film", title: "还没有成片",
      message: "转码后的文件会保存到这里，可随时下载或继续加工。",
      action: h("a", { class: "btn btn-sm btn-primary", href: `#/transcode/${project.id}` }, "开始转码"),
    }));
  } else {
    const list = h("div", { class: "list" });
    project.outputs.forEach((asset) => {
      const item = h("div", { class: "list-item" });
      item.innerHTML = `
        <span class="thumb" style="width:44px;height:30px">${icon("film", 16)}</span>
        <div class="grow">
          <div class="title">${escHtml(asset.fileName)}</div>
          <div class="meta"><span>${bytes(asset.size)}</span><span>${asset.probe?.videoCodec || ""}</span><span>${relTime(asset.createdAt)}</span></div>
        </div>
      `;
      const actions = h("div", { class: "cluster", style: "gap:4px" });
      actions.append(
        h("a", { class: "btn btn-xs btn-ghost", href: `#/player/${project.id}?asset=${asset.id}` }, "播放"),
        h("a", { class: "btn btn-xs btn-ghost", href: `/api/assets/${asset.id}/download` }, "下载"),
      );
      item.appendChild(actions);
      list.appendChild(item);
    });
    body.appendChild(list);
  }
  card.appendChild(body);
  wrap.appendChild(card);
  return wrap;
}

function attachmentsPanel(project, refresh) {
  const wrap = h("div", { class: "stack" });
  const card = h("div", { class: "card" });
  const head = h("div", { class: "card-head" },
    h("h3", {}, "附件"),
    h("span", { class: "spacer" }),
    h("button", { class: "btn btn-sm btn-primary", type: "button", onclick: () => addAttachment(project, refresh) }, "添加附件"),
  );
  card.appendChild(head);
  const body = h("div", { class: "card-body", style: "padding:6px 20px" });
  const atts = project.attachments || [];
  if (!atts.length) {
    body.appendChild(emptyState({
      iconName: "archive", title: "还没有附件",
      message: "可在此追加音频、字幕文件、文档等，与主素材一起归档管理。",
    }));
  } else {
    const list = h("div", { class: "list" });
    atts.forEach((at) => {
      const item = h("div", { class: "list-item" });
      const ic = { audio: "audio", video: "film", subtitle: "wave", document: "archive", other: "archive" }[at.category] || "archive";
      item.innerHTML = `
        <span class="thumb" style="width:44px;height:30px">${icon(ic, 16)}</span>
        <div class="grow">
          <div class="title">${escHtml(at.fileName)} <span class="tag">${escHtml(at.category || "other")}</span></div>
          <div class="meta"><span>${bytes(at.size)}</span><span>${relTime(at.createdAt)}</span></div>
        </div>
      `;
      const actions = h("div", { class: "cluster", style: "gap:4px" });
      const previewable = at.category === "audio" || at.category === "video";
      if (previewable) {
        actions.append(h("a", { class: "btn btn-xs btn-ghost", href: `#/player/${project.id}?asset=${at.id}` }, "播放"));
      }
      actions.append(h("a", { class: "btn btn-xs btn-ghost", href: `/api/assets/${at.id}/download` }, "下载"));
      if (at.category === "subtitle") {
        const imp = h("button", { class: "btn btn-xs btn-soft", type: "button" }, "导入为字幕");
        imp.addEventListener("click", async () => { await importAttachmentAsSubtitle(project, at, refresh); });
        actions.append(imp);
      }
      const delBtn = h("button", { class: "btn btn-xs btn-danger-soft", type: "button", title: "删除附件" }, "删除");
      delBtn.addEventListener("click", async () => {
        const ok = await confirmDialog({ title: "删除附件", message: `确定删除「${at.fileName}」吗？`, confirmText: "删除", danger: true });
        if (!ok) return;
        try {
          await del(`/api/projects/${project.id}/attachments/${at.id}`);
          toast("附件已删除", "", "success", 1800);
          await refresh();
        } catch (err) { toast("删除失败", err.message, "error"); }
      });
      actions.append(delBtn);
      item.appendChild(actions);
      list.appendChild(item);
    });
    body.appendChild(list);
  }
  card.appendChild(body);
  wrap.appendChild(card);
  return wrap;
}

async function addAttachment(project, refresh) {
  const input = h("input", { type: "file", multiple: true, style: "display:none" });
  document.body.appendChild(input);
  input.onchange = async () => {
    const files = Array.from(input.files || []);
    input.remove();
    for (const file of files) {
      const t = toast("正在添加附件", file.name, "info", 0);
      try {
        await upload(`/api/projects/${project.id}/attachments`, file, { category: "auto" });
        t.dismiss();
      } catch (err) {
        t.dismiss();
        toast("添加失败", err.message, "error");
      }
    }
    await refresh();
  };
  input.click();
}

async function importAttachmentAsSubtitle(project, at, refresh) {
  try {
    const res = await fetch(`/api/assets/${at.id}/download`);
    const text = await res.text();
    const fmt = at.fileName.toLowerCase().endsWith(".vtt") ? "vtt" : "srt";
    const r = await post(`/api/projects/${project.id}/subtitle`, { content: text, fileName: at.fileName.replace(/\.[^.]+$/, ""), format: fmt });
    toast("已导入为字幕", `${r.track?.cueCount || 0} 条`, "success");
    await refresh();
  } catch (err) {
    toast("导入失败", err.message, "error");
  }
}

function jobsPanel(jobs) {
  const card = h("div", { class: "card" });
  card.innerHTML = `<div class="card-head"><h3>任务记录</h3></div>`;
  const body = h("div", { class: "card-body", style: "padding:6px 20px" });
  if (!jobs?.length) {
    body.appendChild(emptyState({ iconName: "wave", title: "还没有任务", message: "转写或转码后，这里会记录每次运行的结果与耗时。" }));
  } else {
    const list = h("div", { class: "list" });
    jobs.forEach((job) => list.appendChild(jobRow(job)));
    body.appendChild(list);
  }
  card.appendChild(body);
  return card;
}

function notesPanel(project, refresh) {
  const card = h("div", { class: "card card-pad" });
  card.innerHTML = `<h4 style="margin:0 0 10px;font-size:12.5px;font-weight:600;color:var(--text-2)">项目备注</h4>`;
  const textarea = h("textarea", { class: "textarea", rows: "8", placeholder: "记录素材来源、校对进度、待办事项…" });
  textarea.value = project.notes || "";
  const row = h("div", { class: "cluster", style: "margin-top:12px" });
  const save = h("button", { class: "btn btn-primary btn-sm", type: "button" }, "保存备注");
  save.addEventListener("click", async () => {
    save.disabled = true;
    try {
      await patch(`/api/projects/${project.id}`, { notes: textarea.value });
      toast("备注已保存", "", "success", 1800);
      project.notes = textarea.value;
    } catch (err) {
      toast("保存失败", err.message, "error");
    } finally {
      save.disabled = false;
    }
  });
  row.appendChild(save);
  card.append(textarea, row);
  return card;
}

async function importSubtitle(project, refresh) {
  const content = await new Promise((resolve) => {
    const fileInput = h("input", { type: "file", accept: ".srt,.vtt,.json", style: "display:none" });
    document.body.appendChild(fileInput);
    fileInput.onchange = () => {
      const file = fileInput.files?.[0];
      if (!file) { resolve(null); fileInput.remove(); return; }
      const reader = new FileReader();
      reader.onload = () => { resolve({ name: file.name, text: String(reader.result || "") }); fileInput.remove(); };
      reader.onerror = () => { resolve(null); fileInput.remove(); };
      reader.readAsText(file, "utf-8");
    };
    fileInput.click();
  });
  if (!content) return;
  try {
    const res = await post(`/api/projects/${project.id}/subtitle`, {
      content: content.text,
      fileName: content.name.replace(/\.[^.]+$/, ""),
      format: content.name.toLowerCase().endsWith(".vtt") ? "vtt" : "srt",
    });
    toast("字幕已导入", `${res.track?.cueCount || 0} 条`, "success");
    await refresh();
  } catch (err) {
    toast("导入失败", err.message, "error");
  }
}

function openMenu(project, refresh) {
  const body = h("div", { class: "stack-sm" });
  const rename = h("button", { class: "btn btn-ghost btn-block", type: "button" }, "重命名项目");
  rename.addEventListener("click", () => {
    const input = h("input", { class: "input", value: project.title });
    const modal = openModal({
      title: "重命名项目",
      content: input,
      footer: h("button", { class: "btn btn-primary", type: "button" }, "保存"),
    });
    modal.element.querySelector(".modal-foot button").addEventListener("click", async () => {
      const value = input.value.trim();
      if (!value) return;
      try {
        await patch(`/api/projects/${project.id}`, { title: value });
        modal.close();
        toast("已重命名", value, "success");
        await refresh();
      } catch (err) {
        toast("重命名失败", err.message, "error");
      }
    });
  });

  const toLibrary = h("button", { class: "btn btn-ghost btn-block", type: "button" }, "保存到项目库");
  toLibrary.hidden = !project.temporary;
  toLibrary.addEventListener("click", async () => {
    try {
      await patch(`/api/projects/${project.id}`, { temporary: false });
      toast("已保存到项目库", project.title, "success");
      await refresh();
      await refreshProjects();
    } catch (err) {
      toast("操作失败", err.message, "error");
    }
  });

  const remove = h("button", { class: "btn btn-danger-soft btn-block", type: "button" }, "移入回收站");
  remove.addEventListener("click", async () => {
    const ok = await confirmDialog({
      title: "移入回收站",
      message: `确定把「${project.title}」移到回收站吗？可以随时还原。`,
      confirmText: "移入回收站", danger: true,
    });
    if (!ok) return;
    try {
      await del(`/api/projects/${project.id}`);
      toast("已移入回收站", "可在回收站还原", "success");
      await refreshStats();
      location.hash = "#/projects";
    } catch (err) {
      toast("删除失败", err.message, "error");
    }
  });

  // 分组：统一为文件夹（可嵌套）
  const grouping = h("div", { class: "stack-sm" });
  grouping.append(h("div", { class: "small muted", style: "margin-top:4px" }, "移动到文件夹"));
  const folderSel = h("select", { class: "select" });
  folderSel.append(h("option", { value: "" }, "（未分类）"));
  for (const opt of folderOptions()) folderSel.append(h("option", { value: opt.folder.id }, opt.label));
  folderSel.value = project.folderId || "";
  grouping.append(folderSel);
  const applyBtn = h("button", { class: "btn btn-primary btn-sm btn-block", type: "button" }, "保存分组");
  applyBtn.addEventListener("click", async () => {
    try {
      await patch(`/api/projects/${project.id}`, { folderId: folderSel.value });
      toast("分组已更新", "", "success", 1600);
      await refresh();
    } catch (err) { toast("更新失败", err.message, "error"); }
  });
  grouping.append(applyBtn);

  body.append(rename, toLibrary, grouping, remove);
  openModal({ title: "项目操作", content: body });
}

// ---------------- 右键菜单与快捷操作 ----------------

// openProjectMenu 在项目卡片上右键弹出快捷操作（移动 / 复制 / 删除 / 属性 / 重命名 / 转码 / 转写 / 添加附件）。
function openProjectMenu(x, y, p, selected, refresh) {
  const ids = selected && selected.size && selected.has(p.id) ? [...selected] : [p.id];
  const multi = ids.length > 1;
  menuPopup(x, y, [
    { label: multi ? `打开（${ids.length} 项）` : "打开", icon: "folder", onClick: () => { location.hash = `#/project/${p.id}`; } },
    { label: "转写", icon: "wave", onClick: () => { location.hash = `#/transcribe/${p.id}`; } },
    { label: "转码", icon: "film", onClick: () => { location.hash = `#/transcode/${p.id}`; } },
    { label: "添加附件", icon: "archive", onClick: () => addAttachment(p, refresh) },
    { label: "重命名", icon: "edit", onClick: () => renameProject(p, refresh) },
    { label: "移动到文件夹…", icon: "folder", onClick: () => assignDialog(ids, refresh) },
    { label: "属性", icon: "info", onClick: () => projectProperties(p) },
    { label: multi ? `删除（${ids.length} 项）` : "删除", icon: "trash", danger: true, onClick: () => deleteProject(ids, refresh) },
  ]);
}

async function renameProject(project, refresh) {
  const input = h("input", { class: "input", value: project.title });
  const m = openModal({
    title: "重命名项目", content: input,
    footer: h("button", { class: "btn btn-primary", type: "button" }, "保存"),
  });
  m.element.querySelector(".modal-foot button").addEventListener("click", async () => {
    const v = input.value.trim();
    if (!v) return;
    try {
      await patch(`/api/projects/${project.id}`, { title: v });
      m.close();
      toast("已重命名", v, "success", 1800);
      if (refresh) await refresh();
    } catch (err) { toast("重命名失败", err.message, "error"); }
  });
}

async function deleteProject(ids, refresh) {
  const list = Array.isArray(ids) ? ids : [ids];
  const ok = await confirmDialog({
    title: "移入回收站",
    message: `确定把 ${list.length} 个项目移到回收站吗？可以随时还原。`,
    confirmText: "移入回收站", danger: true,
  });
  if (!ok) return;
  try {
    await post("/api/library/batch", { action: "trash", projectIds: list });
    toast("已移入回收站", `${list.length} 项，可在回收站还原`, "success", 2400);
  } catch (err) {
    toast("删除失败", err.message, "error");
    return;
  }
  await refreshStats();
  if (refresh) await refresh();
}

// descendantFolderIds 返回某文件夹的全部子孙（用于防止移动成环）。
function descendantFolderIds(id) {
  const out = new Set();
  const walk = (pid) => {
    for (const f of state.folders || []) {
      if ((f.parentId || "") !== pid || out.has(f.id)) continue;
      out.add(f.id);
      walk(f.id);
    }
  };
  walk(id);
  return out;
}

// assignDialog 把项目移动到一个文件夹（层级列表，可直接新建）。
async function assignDialog(ids, refresh) {
  const list = Array.isArray(ids) ? ids : [ids];
  const body = h("div", { class: "stack-sm" });
  body.append(h("div", { class: "small muted" }, `${list.length} 个项目将移到所选文件夹`));
  const listHost = h("div", { class: "stack-sm assign-list" });
  body.append(listHost);
  const createBtn = h("button", { class: "btn btn-soft btn-block", type: "button" }, "＋ 新建文件夹");
  body.append(createBtn);
  const m = openModal({ title: "移动到文件夹", content: body });

  const moveTo = async (folderId, label) => {
    try {
      for (const id of list) await patch(`/api/projects/${id}`, { folderId });
      toast("已移动", `${list.length} 个项目 → ${label}`, "success", 1800);
      m.close();
      if (refresh) await refresh();
    } catch (err) { toast("操作失败", err.message, "error"); }
  };

  listHost.append(h("button", {
    class: "btn btn-ghost btn-block", type: "button", style: "justify-content:flex-start",
    onclick: () => moveTo("", "未分类"),
  }, "（未分类 · 移出文件夹）"));
  const options = folderOptions();
  if (!options.length) listHost.append(h("div", { class: "small muted" }, "还没有文件夹，可用下方按钮新建。"));
  for (const opt of options) {
    listHost.append(h("button", {
      class: "btn btn-ghost btn-block", type: "button", style: "justify-content:flex-start",
      onclick: () => moveTo(opt.folder.id, opt.folder.name),
    }, opt.label));
  }

  createBtn.addEventListener("click", () => {
    m.close();
    createFolderDialog("", async () => {
      if (refresh) await refresh();
      assignDialog(list, refresh);
    });
  });
}

// createFolderDialog 新建文件夹；parentId 为空表示建在根目录。
async function createFolderDialog(parentId, refresh) {
  const parent = parentId ? (state.folders || []).find((f) => f.id === parentId) : null;
  const input = h("input", { class: "input", placeholder: "文件夹名称" });
  const m = openModal({
    title: "新建文件夹",
    content: h("div", { class: "stack-sm" },
      h("div", { class: "small muted" }, parent ? `建在「${parent.name}」下` : "建在根目录"),
      input),
    footer: h("button", { class: "btn btn-primary", type: "button" }, "创建"),
  });
  const submit = async () => {
    const v = input.value.trim();
    if (!v) { input.focus(); return; }
    try {
      await post("/api/folders", { name: v, parentId: parentId || "" });
      m.close();
      toast("已创建", v, "success", 1800);
      await refreshFolders();
      if (parentId) expandFolder(parentId);
      if (refresh) await refresh();
    } catch (err) { toast("创建失败", err.message, "error"); }
  };
  m.element.querySelector(".modal-foot button").addEventListener("click", submit);
  input.addEventListener("keydown", (ev) => { if (ev.key === "Enter") submit(); });
  input.focus();
}

// renameFolderDialog 重命名文件夹。
async function renameFolderDialog(folder, refresh) {
  const input = h("input", { class: "input", value: folder.name });
  const m = openModal({
    title: "重命名文件夹", content: input,
    footer: h("button", { class: "btn btn-primary", type: "button" }, "保存"),
  });
  m.element.querySelector(".modal-foot button").addEventListener("click", async () => {
    const v = input.value.trim();
    if (!v) return;
    try {
      await patch(`/api/folders/${folder.id}`, { name: v });
      m.close();
      toast("已重命名", v, "success", 1800);
      await refreshFolders();
      if (refresh) await refresh();
    } catch (err) { toast("重命名失败", err.message, "error"); }
  });
}

// moveFolderDialog 调整文件夹的父级（改变嵌套层级）。
async function moveFolderDialog(folder, refresh) {
  const blocked = descendantFolderIds(folder.id);
  blocked.add(folder.id);
  const body = h("div", { class: "stack-sm" });
  body.append(h("div", { class: "small muted" }, `把「${folder.name}」移动到：`));
  const listHost = h("div", { class: "stack-sm assign-list" });
  body.append(listHost);
  const m = openModal({ title: "移动文件夹", content: body });

  const moveTo = async (parentId, label) => {
    try {
      await patch(`/api/folders/${folder.id}`, { parentId });
      toast("已移动", `${folder.name} → ${label}`, "success", 1800);
      m.close();
      await refreshFolders();
      if (refresh) await refresh();
    } catch (err) { toast("移动失败", err.message, "error"); }
  };

  listHost.append(h("button", {
    class: "btn btn-ghost btn-block", type: "button", style: "justify-content:flex-start",
    onclick: () => moveTo("", "根目录"),
  }, "（根目录）"));
  for (const opt of folderOptions()) {
    if (blocked.has(opt.folder.id)) continue;
    listHost.append(h("button", {
      class: "btn btn-ghost btn-block", type: "button", style: "justify-content:flex-start",
      onclick: () => moveTo(opt.folder.id, opt.folder.name),
    }, opt.label));
  }
}

// deleteFolderNode 删除文件夹：连同子文件夹与其中项目一起进回收站。
async function deleteFolderNode(folder, refresh) {
  const ok = await confirmDialog({
    title: "删除文件夹",
    message: `确定把文件夹「${folder.name}」移到回收站吗？其中的项目与子文件夹会一起移入，可在回收站还原。`,
    confirmText: "移入回收站", danger: true,
  });
  if (!ok) return;
  try {
    await del(`/api/folders/${folder.id}`);
    toast("已移入回收站", folder.name, "success", 1800);
    await refreshFolders();
    if (location.hash.includes(`folder=${folder.id}`)) {
      location.hash = "#/projects";
      return;
    }
    if (refresh) await refresh();
  } catch (err) { toast("删除失败", err.message, "error"); }
}

// projectProperties 展示项目的只读属性。
async function projectProperties(project) {
  let data;
  try {
    data = await get(`/api/projects/${project.id}`);
  } catch (err) {
    toast("读取失败", err.message, "error");
    return;
  }
  const p = data.project;
  const probe = p.media?.probe || {};
  const folder = (state.folders || []).find((f) => f.id === p.folderId);
  const row = (k, v) => `<div class="prop-row"><span>${k}</span><b>${escHtml(String(v ?? "—"))}</b></div>`;
  const body = h("div", { class: "stack-sm" });
  body.innerHTML = `
    ${row("标题", p.title)}
    ${row("状态", ({ draft: "草稿", processing: "处理中", ready: "就绪", failed: "失败" })[p.status] || p.status)}
    ${row("类型", ({ transcribe: "转写", transcode: "转码", info: "信息" })[p.kind] || p.kind)}
    ${row("主文件", p.media?.fileName || "")}
    ${row("文件大小", bytes(p.media?.size || 0))}
    ${row("时长", duration(p.duration))}
    ${row("分辨率", probe.width ? `${probe.width}×${probe.height}` : "—")}
    ${row("视频编码", probe.videoCodec || "—")}
    ${row("音频编码", probe.audioCodec || "—")}
    ${row("字幕", `${(p.subtitles || []).length} 份`)}
    ${row("成片", `${(p.outputs || []).length} 个`)}
    ${row("附件", `${(p.attachments || []).length} 个`)}
    ${row("文件夹", folder ? folder.name : "未分类")}
    ${row("创建时间", dateTime(p.createdAt))}
    ${row("更新时间", dateTime(p.updatedAt))}
    ${row("最近打开", p.openedAt ? dateTime(p.openedAt) : "—")}
  `;
  openModal({ title: "属性", content: body });
}

function truncate(text, n) {
  const value = String(text || "");
  return value.length > n ? value.slice(0, n - 1) + "…" : value;
}














