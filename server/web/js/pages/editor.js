// 字幕编辑器：播放器 + 时间轴 + 逐句编辑，支持批量工具与撤销。
import { get, post } from "../api.js";
import { h, throttle } from "../dom.js";
import {
  icon, toast, timecode, parseTimecode, escHtml, openModal,
} from "../ui.js";


export default {
  async render(root, ctx) {
    const controller = new Editor(root, ctx.id || "");
    await controller.start();
    return () => controller.destroy();
  },
};

class Editor {
  constructor(root, projectId) {
    this.root = root;
    this.projectId = projectId;
    this.project = null;
    this.track = null;
    this.cues = [];
    this.active = -1;
    this.undoStack = [];
    this.redoStack = [];
    this.dirty = false;
    this.zoom = 1;
    this.duration = 0;
    this.player = null;
    this.trackId = "";
    this.mediaUrl = "";
    this.mediaName = "";
    this.follow = true;
    this.loop = false;
    this.disposed = false;
    this.saveTimer = null;
    this.clockRaf = null;
  }

  async start() {
    if (this.projectId) {
      try {
        const data = await get(`/api/projects/${this.projectId}`);
        this.project = data.project;
        this.duration = this.project.duration || this.project.media?.probe?.duration || 0;
        this.mediaName = this.project.media?.fileName || "";
        this.mediaUrl = `/api/assets/${this.project.media?.id}/media`;
        const params = new URLSearchParams(location.hash.split("?")[1] || "");
        const wantTrack = params.get("track") || this.project.subtitles?.[0]?.id || "";
        this.track = (this.project.subtitles || []).find((t) => t.id === wantTrack) || this.project.subtitles?.[0] || null;
        this.trackId = this.track?.id || "";
        if (this.track) await this.loadTrack(this.track);
      } catch (err) {
        toast("无法打开项目", err.message, "error");
        location.hash = "#/projects";
        return;
      }
    }
    this.renderShell();
    this.bindPlayer();
    if (!this.cues.length && !this.projectId) {
      this.showImportHint();
    }
  }

  destroy() {
    this.disposed = true;
    this.stopClock();
    this.flushSave();
    window.removeEventListener("beforeunload", this.onBeforeUnload);
    document.removeEventListener("keydown", this.onKeydown);
    if (this.mediaUrl && this.mediaUrl.startsWith("blob:")) URL.revokeObjectURL(this.mediaUrl);
  }

  // ---------- 数据 ----------
  async loadTrack(track) {
    const data = await get(`/api/projects/${this.projectId}/exports?format=json&track=${track.id}&inline=1`);
    try {
      const parsed = JSON.parse(data.content || "{}");
      this.cues = Array.isArray(parsed.cues) ? parsed.cues : [];
    } catch (_) {
      this.cues = [];
    }
    if (this.duration <= 0 && this.cues.length) {
      this.duration = this.cues[this.cues.length - 1].end;
    }
  }

  async loadCuesFromText(text) {
    const res = await post(`/api/projects/${this.projectId}/subtitle`, {
      content: text,
      format: "srt",
      fileName: this.project ? this.project.title : "imported",
    });
    this.trackId = res.track?.id || "";
    await this.reload();
    toast("字幕已导入", `${res.track?.cueCount || 0} 条`, "success");
  }

  async reload() {
    const data = await get(`/api/projects/${this.projectId}`);
    this.project = data.project;
    this.track = (this.project.subtitles || []).find((t) => t.id === this.trackId) || this.project.subtitles?.[0] || null;
    this.trackId = this.track?.id || "";
    if (this.track) await this.loadTrack(this.track);
    this.renderShell();
    this.bindPlayer();
  }

  snapshot() {
    this.undoStack.push(JSON.stringify(this.cues));
    if (this.undoStack.length > 80) this.undoStack.shift();
    this.redoStack = [];
    this.dirty = true;
    this.updateDirtyBadge();
  }

  undo() {
    if (!this.undoStack.length) return;
    this.redoStack.push(JSON.stringify(this.cues));
    this.cues = JSON.parse(this.undoStack.pop());
    this.dirty = true;
    this.renderCues();
    this.renderTimeline();
    this.updateDirtyBadge();
  }

  redo() {
    if (!this.redoStack.length) return;
    this.undoStack.push(JSON.stringify(this.cues));
    this.cues = JSON.parse(this.redoStack.pop());
    this.dirty = true;
    this.renderCues();
    this.renderTimeline();
    this.updateDirtyBadge();
  }

  updateDirtyBadge() {
    const badge = this.root.querySelector("[data-role=dirty]");
    if (!badge) return;
    badge.hidden = !this.dirty;
  }

  // ---------- 渲染 ----------
  renderShell() {
    this.root.replaceChildren();
    const wrap = h("div", { class: "stack" });

    // 页头
    const head = h("div", { class: "page-head" });
    head.innerHTML = `
      <div style="min-width:0">
        <h1 style="font-size:22px">${escHtml(this.project?.title || this.mediaName || "字幕编辑")}</h1>
        <p class="sub">${this.track ? `当前：${escHtml(this.track.fileName || "字幕")} · ${this.cues.length} 条 · ${String(this.track.format || "srt").toUpperCase()}` : "载入媒体与字幕，开始逐句校对"}</p>
      </div>
      <span class="spacer"></span>
      <div class="actions"></div>
    `;
    const actions = head.querySelector(".actions");
    const dirty = h("span", { class: "tag tag-warning", "data-role": "dirty" }, "未保存");
    dirty.hidden = !this.dirty;
    actions.appendChild(dirty);
    if (this.projectId) {
      actions.append(
        h("a", { class: "btn btn-sm btn-ghost", href: `#/project/${this.projectId}` }, "项目详情"),
        h("button", { class: "btn btn-sm btn-soft", type: "button", onclick: () => this.importSubtitle() }, "导入字幕"),
        h("button", { class: "btn btn-sm btn-primary", type: "button", onclick: () => this.save() }, "保存"),
      );
    } else {
      actions.append(
        h("button", { class: "btn btn-sm btn-ghost", type: "button", onclick: () => this.pickMedia() }, "选择媒体"),
        h("button", { class: "btn btn-sm btn-ghost", type: "button", onclick: () => this.importSubtitle() }, "载入字幕"),
        h("button", { class: "btn btn-sm btn-primary", type: "button", onclick: () => this.exportLocal() }, "导出 SRT"),
      );
    }
    wrap.appendChild(head);

    const layout = h("div", { class: "editor-layout" });
    const main = h("div", { class: "editor-main" });

    // 播放器
    const playerWrap = h("div", { class: "player-wrap" });
    if (this.mediaUrl) {
      const isAudio = !this.project?.media?.probe?.hasVideo;
      if (isAudio) {
        playerWrap.innerHTML = `<div class="player-empty" style="min-height:120px;background:linear-gradient(160deg,#16203a,#0b1020)">
          <div style="text-align:center">${icon("audio", 34)}<div style="margin-top:8px">${escHtml(this.mediaName)}</div></div>
        </div><audio preload="metadata" src="${this.mediaUrl}"></audio>`;
      } else {
        playerWrap.innerHTML = `<video preload="metadata" src="${this.mediaUrl}" playsinline></video><div class="video-sub" data-role="video-sub" hidden></div>`;
      }
    } else {
      playerWrap.innerHTML = `<div class="player-empty">${icon("film", 34)}<div style="margin-top:8px">选择媒体后在浏览器里直接播放</div></div>`;
    }
    main.appendChild(playerWrap);

    // 控制条
    const controls = h("div", { class: "player-controls" });
    controls.innerHTML = `
      <button class="icon-btn" data-role="play" title="播放 / 暂停（空格）">${icon("play", 18)}</button>
      <span class="time-display"><b data-role="cur">00:00,000</b> / <span data-role="dur">${timecode(this.duration)}</span></span>
      <label class="check"><input type="checkbox" data-role="follow" ${this.follow ? "checked" : ""}> 跟随播放</label>
      <label class="check"><input type="checkbox" data-role="loop" ${this.loop ? "checked" : ""}> 单句循环</label>
      <span class="spacer"></span>
      <div class="zoom-row">
        <span class="small muted">缩放</span>
        <input type="range" min="1" max="12" step="0.5" value="${this.zoom}" data-role="zoom">
      </div>
      <button class="icon-btn" data-role="undo" title="撤销（Ctrl+Z）">${icon("refresh", 16)}</button>
    `;
    main.appendChild(controls);

    // 时间轴
    const timelineCard = h("div", { class: "card timeline-card" });
    timelineCard.innerHTML = `<div class="timeline-scroll"><div class="timeline" data-role="timeline"></div></div>`;
    main.appendChild(timelineCard);

    // 字幕列表
    const list = h("div", { class: "cue-list", "data-role": "cue-list" });
    main.appendChild(list);

    // 侧栏
    const side = h("div", { class: "editor-side" });
    side.appendChild(this.toolsCard());
    side.appendChild(this.shortcutCard());
    layout.append(main, side);
    wrap.appendChild(layout);

    this.root.appendChild(wrap);
    this.player = this.root.querySelector("video, audio");
    this.renderCues();
    this.renderTimeline();
    this.updateStats();
  }

  toolsCard() {
    const card = h("div", { class: "card side-block" });
    card.innerHTML = `<h4>批量工具</h4>`;
    const stack = h("div", { class: "stack-sm" });

    stack.appendChild(this.toolButton("整体平移时间轴", () => {
      const input = h("input", { class: "input input-sm", type: "number", step: "0.1", value: "0", placeholder: "秒，可为负" });
      const modal = openModal({
        title: "整体平移时间轴",
        content: h("div", { class: "stack-sm" },
          h("p", { class: "small muted", style: "margin:0" }, "所有字幕同时前移或后移，适合整体对轴。"),
          input),
        footer: h("button", { class: "btn btn-primary btn-sm", type: "button" }, "应用"),
      });
      modal.element.querySelector(".modal-foot button").addEventListener("click", () => {
        const delta = parseFloat(input.value);
        if (!isFinite(delta) || delta === 0) return;
        this.snapshot();
        this.cues = this.cues.map((c) => ({
          ...c,
          start: Math.max(0, c.start + delta),
          end: Math.max(0.1, c.end + delta),
        }));
        modal.close();
        this.renderCues(); this.renderTimeline(); this.updateStats();
        toast("已平移", `${delta > 0 ? "+" : ""}${delta}s`, "success", 1600);
      });
    }));

    stack.appendChild(this.toolButton("查找替换", () => {
      const find = h("input", { class: "input input-sm", placeholder: "查找" });
      const replace = h("input", { class: "input input-sm", placeholder: "替换为（留空即删除）" });
      const modal = openModal({
        title: "批量查找替换",
        content: h("div", { class: "stack-sm" }, find, replace),
        footer: h("button", { class: "btn btn-primary btn-sm", type: "button" }, "替换全部"),
      });
      modal.element.querySelector(".modal-foot button").addEventListener("click", () => {
        const from = find.value;
        if (!from) return;
        const to = replace.value;
        let count = 0;
        this.snapshot();
        this.cues = this.cues.map((c) => {
          if (c.text.includes(from)) {
            count += 1;
            return { ...c, text: c.text.split(from).join(to) };
          }
          return c;
        });
        modal.close();
        this.renderCues();
        toast("替换完成", `影响 ${count} 条字幕`, count ? "success" : "info", 1800);
      });
    }));

    stack.appendChild(this.toolButton("消除时间重叠", () => {
      this.snapshot();
      let prevEnd = -1;
      this.cues = this.cues.map((c) => {
        let start = c.start;
        if (start < prevEnd) start = prevEnd + 0.02;
        const end = Math.max(start + 0.2, c.end);
        prevEnd = end;
        return { ...c, start, end };
      });
      this.renderCues(); this.renderTimeline();
      toast("已消除重叠", "", "success", 1600);
    }));

    stack.appendChild(this.toolButton("合并选中/全部过短句", () => {
      this.snapshot();
      const out = [];
      for (const cue of this.cues) {
        const prev = out[out.length - 1];
        if (prev && prev.end - prev.start < 0.6 && cue.text.length + prev.text.length < 60) {
          prev.text += cue.text;
          prev.end = cue.end;
        } else {
          out.push({ ...cue });
        }
      }
      this.cues = out;
      this.renderCues(); this.renderTimeline(); this.updateStats();
      toast("已合并短句", `剩余 ${out.length} 条`, "success", 1800);
    }));

    card.appendChild(stack);
    return card;
  }

  toolButton(label, onClick) {
    const btn = h("button", { class: "btn btn-ghost btn-sm btn-block", type: "button", style: "justify-content:flex-start" }, label);
    btn.addEventListener("click", onClick);
    return btn;
  }

  shortcutCard() {
    const card = h("div", { class: "card side-block" });
    card.innerHTML = `
      <h4>快捷键</h4>
      <div class="stack-sm small muted">
        <div><span class="kbd">空格</span> 播放 / 暂停</div>
        <div><span class="kbd">↑</span><span class="kbd">↓</span> 上一句 / 下一句</div>
        <div><span class="kbd">Tab</span> 跳到下一句文本框</div>
        <div><span class="kbd">Ctrl</span>+<span class="kbd">S</span> 保存</div>
        <div><span class="kbd">Ctrl</span>+<span class="kbd">Z</span> 撤销</div>
        <div><span class="kbd">Enter</span> 文本框内跳到下一句</div>
      </div>
    `;
    return card;
  }

  // ---------- 播放器 ----------
  bindPlayer() {
    if (!this.player) return;
    this.player.addEventListener("timeupdate", () => this.onTime());
    // 播放中用 rAF 驱动播放头，避免 timeupdate 频率不足导致的画面跳动。
    this.player.addEventListener("play", () => this.startClock());
    this.player.addEventListener("pause", () => this.stopClock());
    this.player.addEventListener("loadedmetadata", () => {
      if (!this.duration || !isFinite(this.duration)) this.duration = this.player.duration || 0;
      this.root.querySelector("[data-role=dur]").textContent = timecode(this.duration);
      this.renderTimeline();
    });
    const playBtn = this.root.querySelector("[data-role=play]");
    playBtn?.addEventListener("click", () => this.togglePlay());
    this.root.querySelector("[data-role=follow]")?.addEventListener("change", (ev) => { this.follow = ev.target.checked; });
    this.root.querySelector("[data-role=loop]")?.addEventListener("change", (ev) => { this.loop = ev.target.checked; });
    this.root.querySelector("[data-role=undo]")?.addEventListener("click", () => this.undo());
    const zoom = this.root.querySelector("[data-role=zoom]");
    zoom?.addEventListener("input", () => {
      this.zoom = parseFloat(zoom.value) || 1;
      this.renderTimeline();
    });
    document.removeEventListener("keydown", this.onKeydown);
    document.addEventListener("keydown", this.onKeydown);
    window.removeEventListener("beforeunload", this.onBeforeUnload);
    window.addEventListener("beforeunload", this.onBeforeUnload);
  }

  onBeforeUnload = (ev) => {
    if (this.dirty) {
      ev.preventDefault();
      ev.returnValue = "";
    }
  };

  togglePlay() {
    if (!this.player) return;
    if (this.player.paused) {
      this.player.play().catch(() => toast("无法播放", "浏览器可能不支持该文件格式，先转成 MP4 试试", "warn"));
    } else {
      this.player.pause();
    }
    this.updatePlayIcon();
  }

  updatePlayIcon() {
    const btn = this.root.querySelector("[data-role=play]");
    if (btn) btn.innerHTML = this.player?.paused === false ? icon("pause", 18) : icon("play", 18);
  }

  onTime = throttle(() => {
    if (!this.player) return;
    const cur = this.player.currentTime;
    this.root.querySelector("[data-role=cur]").textContent = timecode(cur);
    const idx = this.cueAt(cur);
    if (idx !== this.active && idx >= 0) this.setActive(idx, this.follow);
    if (this.loop && idx >= 0 && this.cues[idx] && cur > this.cues[idx].end + 0.06) {
      this.player.currentTime = this.cues[idx].start;
    }
    this.updatePlayhead();
    this.updatePlayIcon();
  }, 60);

  // startClock 播放期间每帧刷新播放头与时间显示（onTime 内部有节流）。
  startClock() {
    if (this.clockRaf || this.disposed) return;
    const tick = () => {
      if (this.disposed || !this.player || this.player.paused) {
        this.clockRaf = null;
        return;
      }
      this.updatePlayhead();
      this.onTime();
      this.clockRaf = requestAnimationFrame(tick);
    };
    this.clockRaf = requestAnimationFrame(tick);
  }

  stopClock() {
    if (this.clockRaf) cancelAnimationFrame(this.clockRaf);
    this.clockRaf = null;
  }

  updatePlayhead() {
    if (!this.player || !(this.duration > 0)) return;
    const ratio = Math.min(1, Math.max(0, this.player.currentTime / this.duration));
    const playhead = this.root.querySelector("[data-role=playhead]");
    if (playhead) playhead.style.left = `${ratio * 100}%`;
    const cur = this.root.querySelector("[data-role=cur]");
    if (cur) cur.textContent = timecode(this.player.currentTime);
  }

  cueAt(seconds) {
    for (let i = this.cues.length - 1; i >= 0; i -= 1) {
      if (this.cues[i].start <= seconds && seconds <= this.cues[i].end + 0.25) return i;
    }
    return -1;
  }

  seekTo(index, play = false) {
    if (index < 0 || index >= this.cues.length || !this.player) return;
    this.player.currentTime = this.cues[index].start;
    this.setActive(index, false);
    this.scrollRowIntoView(this.root.querySelectorAll(".cue-row")[index], true);
    if (play) this.player.play().catch(() => {});
    else this.updatePlayIcon();
  }

  setActive(index, scroll) {
    this.active = index;
    const rows = this.root.querySelectorAll(".cue-row");
    rows.forEach((row, i) => row.classList.toggle("active", i === index));
    this.root.querySelectorAll(".timeline .cue-block").forEach((b, i) => b.classList.toggle("active", i === index));
    if (scroll) this.scrollRowIntoView(rows[index], false);
    this.updateSubtitleOverlay();
  }

  // scrollRowIntoView 把行滚动到列表中部；smooth 用于手动跳转，
  // 跟随播放用即时滚动，且行已在可视区中部附近时不再滚动，
  // 避免连续平滑滚动动画互相打断造成的抖动。
  scrollRowIntoView(row, smooth) {
    const list = this.root.querySelector("[data-role=cue-list]");
    if (!row || !list) return;
    // offsetTop 是相对 offsetParent（可能是页面容器）的，需先换算到列表内容坐标。
    const listRect = list.getBoundingClientRect();
    const rowRect = row.getBoundingClientRect();
    const rowTop = rowRect.top - listRect.top + list.scrollTop;
    const target = Math.max(0, rowTop - (list.clientHeight - rowRect.height) / 2);
    if (!smooth) {
      const delta = target - list.scrollTop;
      if (Math.abs(delta) < list.clientHeight * 0.25) return;
    }
    list.scrollTo({ top: target, behavior: smooth ? "smooth" : "auto" });
  }

  // updateSubtitleOverlay 把当前句字幕叠加显示到视频画面上。
  updateSubtitleOverlay() {
    const overlay = this.root.querySelector("[data-role=video-sub]");
    if (!overlay) return;
    const cue = this.cues[this.active];
    const text = cue ? cue.text.trim() : "";
    overlay.hidden = !text;
    if (text) overlay.textContent = text;
  }

  onKeydown = (ev) => {
    if (this.disposed) return;
    const tag = (ev.target?.tagName || "").toLowerCase();
    const editing = tag === "input" || tag === "textarea" || tag === "select";
    if ((ev.ctrlKey || ev.metaKey) && ev.key.toLowerCase() === "s") {
      ev.preventDefault();
      this.save();
      return;
    }
    if ((ev.ctrlKey || ev.metaKey) && ev.key.toLowerCase() === "z") {
      ev.preventDefault();
      if (ev.shiftKey) this.redo(); else this.undo();
      return;
    }
    if (editing) return;
    if (ev.code === "Space") {
      ev.preventDefault();
      this.togglePlay();
    } else if (ev.code === "ArrowUp") {
      ev.preventDefault();
      this.seekTo(this.active - 1, false);
    } else if (ev.code === "ArrowDown") {
      ev.preventDefault();
      this.seekTo(this.active + 1, false);
    }
  };

  // ---------- 字幕列表 ----------
  renderCues() {
    const list = this.root.querySelector("[data-role=cue-list]");
    if (!list) return;
    if (!this.cues.length) {
      list.innerHTML = `<div class="empty">${icon("wave", 40)}<strong>还没有字幕</strong><p>${this.projectId ? "先在「字幕转写」生成，或导入一份 SRT。" : "选择媒体并载入字幕文件后即可编辑。"}</p></div>`;
      return;
    }
    const frag = document.createDocumentFragment();
    this.cues.forEach((cue, index) => frag.appendChild(this.cueRow(cue, index)));
    list.replaceChildren(frag);
    if (this.active >= 0) this.setActive(this.active, false);
  }

  cueRow(cue, index) {
    const row = h("div", { class: "cue-row" });
    row.innerHTML = `
      <div class="cue-num">${index + 1}</div>
      <div class="cue-times">
        <input data-role="start" value="${timecode(cue.start)}" aria-label="开始时间">
        <input data-role="end" value="${timecode(cue.end)}" aria-label="结束时间">
      </div>
      <textarea class="cue-text" rows="2" spellcheck="false" aria-label="字幕文本">${escHtml(cue.text)}</textarea>
      <div class="cue-actions">
        <button data-role="play" title="从这里播放">${icon("play", 13)}</button>
        <button data-role="stamp-start" title="把开始时间设为当前播放位置">起</button>
        <button data-role="stamp-end" title="把结束时间设为当前播放位置">止</button>
        <button data-role="split" title="从播放位置拆分">拆</button>
        <button data-role="add" title="在下方插入一条">+</button>
        <button data-role="del" class="danger" title="删除">${icon("trash", 13)}</button>
      </div>
    `;
    const textarea = row.querySelector(".cue-text");
    textarea.addEventListener("input", () => {
      this.cues[index].text = textarea.value;
      this.dirty = true;
      this.updateDirtyBadge();
      if (index === this.active) this.updateSubtitleOverlay();
    });
    textarea.addEventListener("focus", () => { this.setActive(index, false); });
    textarea.addEventListener("keydown", (ev) => {
      if (ev.key === "Enter" && !ev.shiftKey) {
        ev.preventDefault();
        this.seekTo(index + 1, false);
        this.root.querySelectorAll(".cue-text")[index + 1]?.focus();
      }
      if (ev.key === "Tab" && !ev.shiftKey) {
        ev.preventDefault();
        this.seekTo(index + 1, false);
        this.root.querySelectorAll(".cue-text")[index + 1]?.focus();
      }
    });
    row.querySelectorAll(".cue-times input").forEach((input) => {
      input.addEventListener("focus", () => this.setActive(index, false));
      input.addEventListener("change", () => {
        const value = parseTimecode(input.value);
        if (!isFinite(value) || value < 0) {
          input.value = timecode(this.cues[index][input.dataset.role]);
          toast("时间格式无法识别", "可用 00:00:01,250 或 12.5", "warn");
          return;
        }
        this.snapshot();
        this.cues[index][input.dataset.role] = value;
        if (this.cues[index].end <= this.cues[index].start) {
          this.cues[index].end = this.cues[index].start + 0.3;
        }
        this.refreshTimes(index);
        this.renderTimeline();
      });
    });
    row.querySelector("[data-role=play]").addEventListener("click", () => this.seekTo(index, true));
    row.querySelector("[data-role=stamp-start]").addEventListener("click", () => this.stamp(index, "start"));
    row.querySelector("[data-role=stamp-end]").addEventListener("click", () => this.stamp(index, "end"));
    row.querySelector("[data-role=split]").addEventListener("click", () => this.splitCue(index));
    row.querySelector("[data-role=add]").addEventListener("click", () => this.insertCue(index));
    row.querySelector("[data-role=del]").addEventListener("click", () => this.deleteCue(index));
    row.addEventListener("click", (ev) => {
      if (ev.target.closest("textarea, input, button")) return;
      this.setActive(index, false);
    });
    return row;
  }

  refreshTimes(index) {
    const row = this.root.querySelectorAll(".cue-row")[index];
    if (!row) return;
    row.querySelector("[data-role=start]").value = timecode(this.cues[index].start);
    row.querySelector("[data-role=end]").value = timecode(this.cues[index].end);
  }

  stamp(index, key) {
    if (!this.player) return;
    this.snapshot();
    this.cues[index][key] = this.player.currentTime;
    if (this.cues[index].end <= this.cues[index].start) this.cues[index].end = this.cues[index].start + 0.4;
    this.refreshTimes(index);
    this.renderTimeline();
  }

  splitCue(index) {
    if (!this.player) return;
    const at = this.player.currentTime;
    const cue = this.cues[index];
    if (at <= cue.start + 0.1 || at >= cue.end - 0.1) {
      toast("拆分位置无效", "请把播放头移到该句中间再拆分", "warn");
      return;
    }
    const text = cue.text;
    const ratio = (at - cue.start) / (cue.end - cue.start);
    const cut = Math.max(1, Math.round(text.length * ratio));
    this.snapshot();
    const first = { ...cue, end: at, text: text.slice(0, cut).trim() };
    const second = { ...cue, start: at, text: text.slice(cut).trim() };
    this.cues.splice(index, 1, first, second);
    this.renderCues(); this.renderTimeline(); this.updateStats();
  }

  insertCue(index) {
    this.snapshot();
    const base = this.cues[index];
    const start = base ? base.end : 0;
    this.cues.splice(index + 1, 0, { start, end: start + 1.5, text: "" });
    this.renderCues(); this.renderTimeline();
    const rows = this.root.querySelectorAll(".cue-text");
    rows[index + 1]?.focus();
  }

  deleteCue(index) {
    this.snapshot();
    this.cues.splice(index, 1);
    this.active = -1;
    this.renderCues(); this.renderTimeline(); this.updateStats();
  }

  updateStats() {
    const chars = this.cues.reduce((n, c) => n + c.text.replace(/\n/g, "").length, 0);
    const el = this.root.querySelector("[data-role=stats]");
    if (el) el.textContent = `${this.cues.length} 条 · ${chars} 字`;
  }

  // ---------- 时间轴 ----------
  renderTimeline() {
    const tl = this.root.querySelector("[data-role=timeline]");
    if (!tl) return;
    const total = Math.max(this.duration || 0, this.cues.at(-1)?.end || 0, 1);
    const width = Math.max(100, Math.round(100 * this.zoom));
    tl.style.width = width + "%";
    const frag = document.createDocumentFragment();

    const ruler = h("div", { class: "ruler" });
    const ticks = niceTicks(total, width);
    ticks.forEach((t) => {
      const span = document.createElement("span");
      span.style.left = `${(t / total) * 100}%`;
      span.textContent = shortTime(t);
      ruler.appendChild(span);
      const mark = document.createElement("i");
      mark.style.left = `${(t / total) * 100}%`;
      ruler.appendChild(mark);
    });
    frag.appendChild(ruler);

    // 重叠字幕分到不同「泳道」，避免时间轴上互相覆盖。
    const lanes = [];
    this.cues.forEach((cue, index) => {
      let lane = lanes.findIndex((end) => cue.start >= end - 0.001);
      if (lane < 0) {
        lane = lanes.length;
        lanes.push(0);
      }
      lanes[lane] = cue.end;
      const block = h("div", { class: "cue-block", "data-index": index, title: cue.text });
      block.style.left = `${(cue.start / total) * 100}%`;
      block.style.width = `${Math.max(0.15, ((cue.end - cue.start) / total) * 100)}%`;
      block.style.top = `${26 + lane * 18}px`;
      block.textContent = cue.text.replace(/\n/g, " ") || "（空）";
      block.innerHTML += `<span class="handle left"></span><span class="handle right"></span>`;
      block.addEventListener("click", (ev) => {
        ev.stopPropagation();
        this.seekTo(index, false);
      });
      this.bindDrag(block, index, "move", total);
      block.querySelector(".handle.left").addEventListener("pointerdown", (ev) => {
        ev.stopPropagation();
        this.bindDrag(block, index, "start", total, ev);
      });
      block.querySelector(".handle.right").addEventListener("pointerdown", (ev) => {
        ev.stopPropagation();
        this.bindDrag(block, index, "end", total, ev);
      });
      frag.appendChild(block);
    });

    const playhead = h("div", { class: "playhead", "data-role": "playhead" });
    if (this.player && total > 0) playhead.style.left = `${(this.player.currentTime / total) * 100}%`;
    frag.appendChild(playhead);

    const note = h("div", { class: "wave-note" }, "拖动色块调时间 · 拖两端改起止");
    frag.appendChild(note);
    tl.replaceChildren(frag);

    // 点击空白处跳转
    tl.addEventListener("click", (ev) => {
      if (ev.target.closest(".cue-block")) return;
      const rect = tl.getBoundingClientRect();
      const ratio = (ev.clientX - rect.left) / rect.width;
      if (this.player) this.player.currentTime = Math.max(0, Math.min(total, ratio * total));
    });
  }

  bindDrag(block, index, mode, total, downEvent) {
    const startX = downEvent ? downEvent.clientX : 0;
    const original = { ...this.cues[index] };
    const rect = this.root.querySelector("[data-role=timeline]").getBoundingClientRect();
    let moved = false;
    let snapshotTaken = false;

    const onMove = (ev) => {
      const dx = ev.clientX - startX;
      if (!moved && Math.abs(dx) < 2) return;
      moved = true;
      if (!snapshotTaken) { this.snapshot(); snapshotTaken = true; }
      const delta = (dx / rect.width) * total;
      const cue = this.cues[index];
      if (mode === "move") {
        const len = original.end - original.start;
        cue.start = clamp(original.start + delta, 0, total - len);
        cue.end = cue.start + len;
      } else if (mode === "start") {
        cue.start = clamp(original.start + delta, 0, cue.end - 0.1);
      } else {
        cue.end = clamp(original.end + delta, cue.start + 0.1, total);
      }
      block.style.left = `${(cue.start / total) * 100}%`;
      block.style.width = `${Math.max(0.15, ((cue.end - cue.start) / total) * 100)}%`;
      this.refreshTimes(index);
    };
    const onUp = () => {
      window.removeEventListener("pointermove", onMove);
      window.removeEventListener("pointerup", onUp);
      if (moved) {
        this.renderTimeline();
        this.active = index;
      }
    };
    if (downEvent) {
      window.addEventListener("pointermove", onMove);
      window.addEventListener("pointerup", onUp);
    } else {
      block.addEventListener("pointerdown", (ev) => {
        if (ev.target.closest(".handle")) return;
        ev.preventDefault();
        const orig = { ...this.cues[index] };
        const startClientX = ev.clientX;
        let localMoved = false;
        let snap = false;
        const move = (e) => {
          const d = e.clientX - startClientX;
          if (!localMoved && Math.abs(d) < 2) return;
          localMoved = true;
          if (!snap) { this.snapshot(); snap = true; }
          const delta = (d / rect.width) * total;
          const len = orig.end - orig.start;
          this.cues[index].start = clamp(orig.start + delta, 0, total - len);
          this.cues[index].end = this.cues[index].start + len;
          block.style.left = `${(this.cues[index].start / total) * 100}%`;
          block.style.width = `${Math.max(0.15, (len / total) * 100)}%`;
          this.refreshTimes(index);
        };
        const up = () => {
          window.removeEventListener("pointermove", move);
          window.removeEventListener("pointerup", up);
          if (localMoved) { this.renderTimeline(); this.active = index; }
        };
        window.addEventListener("pointermove", move);
        window.addEventListener("pointerup", up);
      });
    }
  }

  // ---------- 保存 / 导入 / 导出 ----------
  scheduleSave() {
    clearTimeout(this.saveTimer);
    this.saveTimer = setTimeout(() => this.flushSave(), 3000);
  }

  async flushSave() {
    if (!this.dirty || !this.projectId || !this.trackId) return;
    try {
      await this.persist();
    } catch (_) { /* 下次再试 */ }
  }

  async persist() {
    const res = await post(`/api/projects/${this.projectId}/subtitle`, {
      id: this.trackId,
      cues: this.cues,
      format: this.track?.format || "srt",
      language: this.track?.language || "",
      fileName: this.track?.fileName?.replace(/\.[^.]+$/, "") || this.project?.title,
    });
    this.dirty = false;
    this.updateDirtyBadge();
    this.track = res.track;
    this.trackId = res.track?.id || this.trackId;
    return res;
  }

  async save() {
    if (!this.projectId) {
      this.exportLocal();
      return;
    }
    if (!this.trackId) {
      // 首次保存：建一份新字幕。
      const res = await post(`/api/projects/${this.projectId}/subtitle`, {
        cues: this.cues,
        format: "srt",
        fileName: this.project?.title || "字幕",
      });
      this.trackId = res.track?.id || "";
      this.track = res.track;
    } else {
      await this.persist();
    }
    toast("已保存", `${this.cues.length} 条字幕`, "success", 1800);
  }

  exportLocal() {
    const body = this.cues.map((cue, i) => `${i + 1}\n${timecode(cue.start)} --> ${timecode(cue.end)}\n${cue.text}\n`).join("\n");
    const blob = new Blob([body], { type: "application/x-subrip;charset=utf-8" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = (this.project?.title || this.mediaName || "subtitle") + ".srt";
    document.body.appendChild(a);
    a.click();
    a.remove();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  }

  importSubtitle() {
    if (!this.projectId) {
      const input = document.createElement("input");
      input.type = "file";
      input.accept = ".srt,.vtt";
      input.onchange = () => {
        const file = input.files?.[0];
        if (!file) return;
        const reader = new FileReader();
        reader.onload = () => {
          this.cues = parseSubtitleText(String(reader.result || ""));
          this.renderCues(); this.renderTimeline(); this.updateStats();
          toast("已载入字幕", `${this.cues.length} 条`, "success");
        };
        reader.readAsText(file, "utf-8");
      };
      input.click();
      return;
    }
    const input = document.createElement("input");
    input.type = "file";
    input.accept = ".srt,.vtt,.json";
    input.onchange = async () => {
      const file = input.files?.[0];
      if (!file) return;
      const reader = new FileReader();
      reader.onload = async () => {
        try {
          await this.loadCuesFromText(String(reader.result || ""));
        } catch (err) {
          toast("导入失败", err.message, "error");
        }
      };
      reader.readAsText(file, "utf-8");
    };
    input.click();
  }

  pickMedia() {
    const input = document.createElement("input");
    input.type = "file";
    input.accept = "video/*,audio/*";
    input.onchange = () => {
      const file = input.files?.[0];
      if (!file) return;
      this.mediaName = file.name;
      this.mediaUrl = URL.createObjectURL(file);
      this.renderShell();
      this.bindPlayer();
    };
    input.click();
  }

  showImportHint() {
    openModal({
      title: "开始编辑字幕",
      content: h("div", { class: "stack-sm" },
        h("p", { style: "margin:0" }, "选择媒体文件与字幕文件，即可在浏览器里边听边改。也可以从「项目库」打开一个已完成转写的项目。"),
        h("div", { class: "cluster" },
          h("button", { class: "btn btn-ghost btn-sm", type: "button", onclick: () => { this.pickMedia(); } }, "选择媒体"),
          h("button", { class: "btn btn-primary btn-sm", type: "button", onclick: () => { this.importSubtitle(); } }, "载入字幕"),
        ),
      ),
    });
  }
}

function parseSubtitleText(text) {
  const source = String(text || "").replace(/\r/g, "").replace(/^\uFEFF/, "");
  const cues = [];
  source.split(/\n{2,}/).forEach((block) => {
    const lines = block.split("\n");
    const idx = lines.findIndex((line) => line.includes("-->"));
    if (idx < 0) return;
    const [left, rightRaw] = lines[idx].split("-->");
    const right = (rightRaw || "").trim().split(/\s+/)[0];
    const start = parseTimecode(left.trim());
    const end = parseTimecode(right);
    if (!isFinite(start) || !isFinite(end)) return;
    const body = lines.slice(idx + 1).join("\n").trim();
    if (body) cues.push({ start, end, text: body });
  });
  return cues;
}

function clamp(value, min, max) {
  return Math.max(min, Math.min(max, value));
}

function shortTime(seconds) {
  const total = Math.round(seconds);
  const m = Math.floor(total / 60);
  const s = total % 60;
  return `${m}:${String(s).padStart(2, "0")}`;
}

/** 选出合适的刻度间隔，保证标签不过密。 */
function niceTicks(total, zoomPct) {
  const target = Math.max(6, Math.round((zoomPct / 100) * 14));
  const raw = total / target;
  const steps = [1, 2, 5, 10, 15, 30, 60, 120, 300, 600];
  const step = steps.find((s) => s >= raw) || 900;
  const ticks = [];
  for (let t = 0; t <= total + 0.001; t += step) ticks.push(t);
  return ticks;
}

