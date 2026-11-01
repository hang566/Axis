// 设置：识别引擎、字幕偏好、存储与环境自检。
import { get, post } from "../api.js";
import { h } from "../dom.js";
import { icon, toast, bytes, escHtml, openModal, applyTheme } from "../ui.js";
import { state, loadSystem } from "../store.js";

const TABS = [
  ["general", "通用"],
  ["engine", "识别引擎"],
  ["subtitle", "字幕偏好"],
  ["storage", "存储与维护"],
  ["about", "关于与环境"],
];

export default {
  async render(root) {
    const [settingsResp, env] = await Promise.all([
      get("/api/settings"),
      get("/api/system/check").catch(() => null),
    ]);
    let settings = { ...state.defaults, ...settingsResp.settings };
    let current = "general";

    const page = h("div", { class: "stack" });
    const head = h("div", { class: "page-head" });
    head.innerHTML = `
      <div><h1>设置</h1><p class="sub">这里的修改会立即保存。识别引擎变更后，下一次任务自动生效，无需重启。</p></div>
    `;
    page.appendChild(head);

    const grid = h("div", { class: "settings-grid" });
    const nav = h("div", { class: "settings-nav" });
    const panel = h("div", { class: "settings-panel" });
    TABS.forEach(([key, label]) => {
      const btn = h("button", { class: key === current ? "active" : "", type: "button" }, label);
      btn.addEventListener("click", () => {
        current = key;
        nav.querySelectorAll("button").forEach((b) => b.classList.toggle("active", b === btn));
        renderPanel();
      });
      nav.appendChild(btn);
    });
    grid.append(nav, panel);
    page.appendChild(grid);

    const save = async (patchData, message) => {
      settings = { ...settings, ...patchData };
      try {
        await post("/api/settings", settings);
        window.__settings = settings;
        if (patchData.theme) applyTheme(patchData.theme);
        if (message) toast("已保存", message, "success", 1800);
        await loadSystem().catch(() => {});
      } catch (err) {
        toast("保存失败", err.message, "error");
      }
    };

    const renderPanel = () => {
      panel.replaceChildren();
      if (current === "general") panel.appendChild(generalPanel(settings, save));
      if (current === "engine") panel.appendChild(enginePanel(settings, save, env));
      if (current === "subtitle") panel.appendChild(subtitlePanel(settings, save));
      if (current === "storage") panel.appendChild(storagePanel(env));
      if (current === "about") panel.appendChild(aboutPanel(env));
    };
    renderPanel();
    root.replaceChildren(page);
    return null;
  },
};

function section(title, description, content) {
  const card = h("div", { class: "card" });
  card.innerHTML = `
    <div class="card-head" style="display:block;padding-bottom:12px">
      <h3 style="margin-bottom:2px">${escHtml(title)}</h3>
      <p class="small muted" style="margin:0">${escHtml(description || "")}</p>
    </div>
  `;
  const body = h("div", { class: "card-body" });
  body.appendChild(content);
  card.appendChild(body);
  return card;
}

function rowField(label, control, hint) {
  const el = h("div", { class: "field", style: "margin-bottom:14px" });
  el.append(h("label", {}, label), control);
  if (hint) el.appendChild(h("span", { class: "field-hint" }, hint));
  return el;
}

function generalPanel(settings, save) {
  const wrap = h("div", { class: "stack" });

  const theme = h("div", { class: "segmented" });
  [["auto", "跟随系统"], ["light", "浅色"], ["dark", "深色"]].forEach(([value, label]) => {
    const btn = h("button", { class: settings.theme === value ? "active" : "", type: "button" }, label);
    btn.addEventListener("click", () => {
      theme.querySelectorAll("button").forEach((b) => b.classList.toggle("active", b === btn));
      save({ theme: value }, "外观：" + label);
    });
    theme.appendChild(btn);
  });

  const confirmDelete = h("input", { type: "checkbox", checked: settings.confirmDelete !== false });
  confirmDelete.addEventListener("change", () => save({ confirmDelete: confirmDelete.checked }, ""));
  const autoOpen = h("input", { type: "checkbox", checked: settings.autoOpenBrowser !== false });
  autoOpen.addEventListener("change", () => save({ autoOpenBrowser: autoOpen.checked }, ""));

  wrap.append(
    section("外观", "界面风格，默认跟随系统深浅色。", rowField("主题", theme)),
    section("操作习惯", "影响删除确认与启动行为。",
      h("div", { class: "stack-sm" },
        h("label", { class: "check" }, confirmDelete, h("span", {}, "删除项目 / 字幕前弹出确认")),
        h("label", { class: "check" }, autoOpen, h("span", {}, "启动工作台时自动打开浏览器")),
      ),
    ),
  );
  return wrap;
}

function enginePanel(settings, save, env) {
  const wrap = h("div", { class: "stack" });
  const installed = env?.installedModels || [];

  const engine = h("div", { class: "segmented" });
  [["local", "本地引擎"], ["remote", "在线接口"], ["cli", "命令行工具"]].forEach(([value, label]) => {
    const btn = h("button", { class: settings.engine === value ? "active" : "", type: "button" }, label);
    btn.addEventListener("click", () => {
      engine.querySelectorAll("button").forEach((b) => b.classList.toggle("active", b === btn));
      save({ engine: value }, "识别引擎：" + label);
    });
    engine.appendChild(btn);
  });

  const lang = h("select", { class: "select" });
  [["", "自动检测"], ["zh", "中文"], ["en", "English"], ["ja", "日本語"], ["ko", "한국어"], ["yue", "粤语"]].forEach(([value, label]) => {
    lang.appendChild(h("option", { value, selected: settings.language === value }, label));
  });
  lang.addEventListener("change", () => save({ language: lang.value }, ""));

  const model = h("select", { class: "select" });
  model.appendChild(h("option", { value: "" }, "自动选择（推荐）"));
  installed.forEach((m) => {
    model.appendChild(h("option", { value: m.id, selected: settings.modelId === m.id }, m.id));
  });
  model.addEventListener("change", () => save({ modelId: model.value }, ""));

  const timeout = h("input", { class: "input", type: "number", min: "60", max: "7200", value: settings.timeoutSeconds || 1800 });
  timeout.addEventListener("change", () => save({ timeoutSeconds: Number(timeout.value) || 1800 }, ""));

  const chunk = h("select", { class: "select" });
  [[0, "自动（推荐）"], [5, "5 分钟"], [10, "10 分钟"], [15, "15 分钟"], [30, "30 分钟"]].forEach(([value, label]) => {
    chunk.appendChild(h("option", { value, selected: Number(settings.chunkMinutes) === value }, label));
  });
  chunk.addEventListener("change", () => save({ chunkMinutes: Number(chunk.value) }, ""));

  wrap.appendChild(section("本地识别引擎", "sherpa-onnx 在本机运行，不上传任何音频；模型只需下载一次。",
    h("div", {},
      rowField("引擎类型", engine),
      rowField("默认模型", model, installed.length ? `已安装 ${installed.length} 个模型` : "还没有本地模型，请到「模型中心」下载"),
      rowField("识别语言", lang, "留空让模型自动判断，通常更省心"),
    ),
  ));

  wrap.appendChild(section("性能与长音频", "长音频会按此长度切分，并尽量切在停顿处。",
    h("div", {},
      rowField("单次任务超时（秒）", timeout, "超过这个时间会中断并给出提示；长音频建议 3600"),
      rowField("分段长度", chunk, "切得越短越省内存，但边界处的识别可能略有损失"),
    ),
  ));

  // 在线接口
  const endpoint = h("input", { class: "input", value: settings.remoteEndpoint || "", placeholder: "https://api.openai.com/v1/audio/transcriptions" });
  const remoteModel = h("input", { class: "input", value: settings.remoteModel || "", placeholder: "whisper-1" });
  const apiKey = h("input", { class: "input", type: "password", value: settings.apiKey || "", placeholder: "sk-…（也可留空，改用环境变量）" });
  const apiKeyEnv = h("input", { class: "input", value: settings.apiKeyEnv || "OPENAI_API_KEY" });
  endpoint.addEventListener("change", () => save({ remoteEndpoint: endpoint.value }, ""));
  remoteModel.addEventListener("change", () => save({ remoteModel: remoteModel.value }, ""));
  apiKey.addEventListener("change", () => save({ apiKey: apiKey.value }, ""));
  apiKeyEnv.addEventListener("change", () => save({ apiKeyEnv: apiKeyEnv.value }, ""));

  wrap.appendChild(section("在线识别接口（可选）", "任何兼容 OpenAI /v1/audio/transcriptions 的服务都可以填在这里。国内网络通常需要代理或中转地址。",
    h("div", {},
      rowField("接口地址", endpoint),
      rowField("模型名称", remoteModel),
      rowField("API Key", apiKey, "密钥保存在本机 data/settings.json，不会上传到别处"),
      rowField("环境变量名", apiKeyEnv, "如果不想把密钥写进配置，可设置环境变量并在上方留空"),
    ),
  ));
  return wrap;
}

function subtitlePanel(settings, save) {
  const wrap = h("div", { class: "stack" });
  const number = (key, value, min, max, suffix = "") => {
    const input = h("input", { class: "input", type: "number", value, min, max });
    input.addEventListener("change", () => {
      const v = Number(input.value);
      if (!isFinite(v)) return;
      save({ [key]: v }, "");
    });
    return suffix ? h("div", { style: "display:flex;gap:6px;align-items:center" }, input, h("span", { class: "small muted" }, suffix)) : input;
  };

  const merge = h("input", { type: "checkbox", checked: settings.subSentenceMerge !== false });
  merge.addEventListener("change", () => save({ subSentenceMerge: merge.checked }, ""));
  const strip = h("input", { type: "checkbox", checked: settings.subStripPunctuation !== false });
  strip.addEventListener("change", () => save({ subStripPunctuation: strip.checked }, ""));

  const rows = h("div", {});
  rows.append(
    rowField("单行最大字符数", number("subMaxLineLength", settings.subMaxLineLength || 42, 10, 120), "中文一般 16~42，英文可放宽"),
    rowField("单条最多行数", number("subMaxLines", settings.subMaxLines || 2, 1, 4), "常见为 2 行"),
    rowField("单条最长时长（秒）", number("subMaxCueSeconds", settings.subMaxCueSeconds || 6, 1, 20)),
    rowField("单条最短时长（毫秒）", number("subMinCueMs", settings.subMinCueMs || 700, 100, 3000), "过短的字幕会与相邻句合并"),
    rowField("最大停顿（毫秒）", number("subMaxPauseMs", settings.subMaxPauseMs || 1500, 100, 5000), "停顿超过此值就断句"),
  );

  const toggles = h("div", { class: "stack-sm", style: "margin-top:6px" },
    h("label", { class: "check" }, merge, h("span", {}, "按句合并，避免逐词碎片化")),
    h("label", { class: "check" }, strip, h("span", {}, "输出时去掉标点（断句仍按标点判断）")),
  );

  wrap.append(
    section("字幕排版", "这些是默认值，转写时还能按预设临时覆盖。", rows),
    section("断句习惯", "", toggles),
  );
  return wrap;
}

function storagePanel(env) {
  const wrap = h("div", { class: "stack" });
  const disk = env?.disk || {};
  const stats = state.stats?.library || {};

  const usage = h("div", { class: "stack" });
  usage.innerHTML = `
    <div class="stream-data">
      <div class="item"><span class="k">素材库</span><span class="v">${bytes(stats.mediaBytes || 0)}</span></div>
      <div class="item"><span class="k">字幕文件</span><span class="v">${bytes(stats.subsBytes || 0)}</span></div>
      <div class="item"><span class="k">成片</span><span class="v">${bytes(stats.outBytes || 0)}</span></div>
      <div class="item"><span class="k">临时文件</span><span class="v">${bytes(stats.tempBytes || 0)}</span></div>
      <div class="item"><span class="k">磁盘剩余</span><span class="v">${bytes(disk.freeBytes || 0)}</span></div>
    </div>
  `;

  const paths = h("div", { class: "stack-sm" });
  paths.innerHTML = `
    <div class="kv"><span class="k">数据目录</span><span class="v mono small">${escHtml(state.system?.dataDir || env?.dataDir || "")}</span></div>
    <div class="kv"><span class="k">模型目录</span><span class="v mono small">${escHtml(state.system?.modelsDir || env?.modelsDir || "")}</span></div>
  `;

  const cleanup = h("button", { class: "btn btn-sm btn-ghost", type: "button" }, "清理临时文件");
  cleanup.addEventListener("click", () => {
    openModal({
      title: "清理临时文件",
      content: h("p", {}, "临时文件包括上传缓存、缩略图与中断任务留下的中间音频。清理不会影响项目与字幕，程序会在启动时自动清理 24 小时前的临时文件。"),
      footer: h("button", { class: "btn btn-primary btn-sm", type: "button", onclick: () => {
        document.getElementById("modal-root").hidden = true;
        toast("提示", "重启工作台即可完成自动清理；也可直接删除 data/temp 目录下的内容", "info", 6000);
      }}, "知道了"),
    });
  });

  wrap.append(
    section("空间占用", "所有数据都在本机，复制整个程序目录即可完成迁移或备份。", usage),
    section("目录位置", "", h("div", {}, paths, h("div", { class: "cluster", style: "margin-top:14px" }, cleanup))),
    section("备份建议", "定期把整个程序目录（含 data 与 models）复制到其它磁盘即可。", h("p", { class: "small muted", style: "margin:0" },
      "项目文件是纯 JSON + 字幕文本 + 媒体文件，不依赖数据库；library.json 损坏时程序会从项目目录自动重建索引。")),
  );
  return wrap;
}

function aboutPanel(env) {
  const wrap = h("div", { class: "stack" });
  const rows = [
    ["版本", state.system?.version || ""],
    ["平台", state.system?.platform || ""],
    ["ffmpeg", env?.ffmpeg?.ok ? env.ffmpeg.version : env?.ffmpeg?.message || "缺失"],
    ["ffprobe", env?.ffprobe?.ok ? env.ffprobe.version : env?.ffprobe?.message || "缺失"],
    ["Python", env?.python?.ok ? env.python.version : env?.python?.message || "缺失"],
    ["本地模型", `${(env?.installedModels || []).length} 个`],
  ];
  const table = h("div", { class: "stack-sm" });
  table.innerHTML = rows.map(([k, v]) => `
    <div class="kv"><span class="k">${escHtml(k)}</span><span class="v small">${escHtml(v || "-")}</span></div>
  `).join("");

  const problems = env?.problems || [];
  const problemBox = problems.length
    ? h("div", { class: "alert alert-warning" },
        h("span", { class: "ic", html: icon("alert", 18) }),
        h("div", {}, h("strong", {}, "需要处理"), problems.map((p) => h("div", {}, p))),
      )
    : h("div", { class: "alert alert-success" },
        h("span", { class: "ic", html: icon("check", 18) }),
        h("div", {}, h("strong", {}, "环境就绪"), "本地转写、分析与转码都可以直接使用。"),
      );

  wrap.append(
    section("运行环境", "启动自检的结果；缺少组件时下方会给出处理建议。", h("div", {}, table, h("div", { style: "margin-top:14px" }, problemBox))),
    section("关于", "视听工作台是一个单文件本地应用：Go 负责调度，ffmpeg 负责解码，识别模型可按需切换。", h("p", { class: "small muted", style: "margin:0" },
      "数据完全保存在本机，不联网也能转写。若需要更高的准确率，可以在「模型中心」下载更大的模型，或在设置里接入在线接口。")),
  );
  return wrap;
}
