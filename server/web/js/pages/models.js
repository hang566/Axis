// 模型中心：已安装模型、可下载清单、下载进度与设为默认。
import { get, post } from "../api.js";
import { h } from "../dom.js";
import { icon, toast, bytes, escHtml, confirmDialog } from "../ui.js";
import { state, loadSystem } from "../store.js";

export default {
  async render(root) {
    let data = await get("/api/models").catch(() => null);
    const page = h("div", { class: "stack" });

    const head = h("div", { class: "page-head" });
    head.innerHTML = `
      <div>
        <h1>模型中心</h1>
        <p class="sub">本地识别需要语音模型。中文推荐 SenseVoice（多语种、自带标点）；下载后无需重启，下一次任务自动生效。</p>
      </div>
      <span class="spacer"></span>
      <div class="actions">
        <button class="btn btn-sm btn-ghost" data-role="refresh">${icon("refresh", 15)} 刷新</button>
      </div>
    `;
    page.appendChild(head);

    // 环境提示
    const env = state.env;
    if (env && !env.python?.ok) {
      page.appendChild(h("div", { class: "alert alert-warning" },
        h("span", { class: "ic", html: icon("alert", 18) }),
        h("div", {},
          h("strong", {}, "本地识别需要 Python + sherpa-onnx"),
          h("div", { html: escHtml(env.python?.message || "未检测到 Python") }),
          h("div", { class: "small", style: "margin-top:4px", html: escHtml(env.python?.fix || "") }),
        ),
      ));
    }

    // 已安装
    const installedCard = h("div", { class: "card" });
    installedCard.innerHTML = `<div class="card-head"><h3>已安装</h3><span class="spacer"></span><span class="small muted" data-role="dir"></span></div>`;
    const installedBody = h("div", { class: "card-body", style: "padding:6px 20px" });
    installedCard.appendChild(installedBody);
    page.appendChild(installedCard);

    // 可下载
    const storeTitle = h("div", { class: "section-title" }, h("h2", {}, "可下载模型"), h("span", { class: "spacer" }), h("span", { class: "muted" }, "来自 sherpa-onnx 官方发布"));
    const storeGrid = h("div", { class: "model-grid" });
    page.appendChild(storeTitle);
    page.appendChild(storeGrid);

    const render = (payload) => {
      data = payload;
      const installed = payload.installed || [];
      const models = payload.models || [];
      page.querySelector("[data-role=dir]").textContent = payload.modelsDir || "";

      // 已安装
      installedBody.replaceChildren();
      if (!installed.length) {
        installedBody.appendChild(h("div", { class: "empty", style: "padding:24px" },
          h("strong", {}, "还没有本地模型"),
          h("p", {}, "从下方选一个下载即可开始离线识别。"),
        ));
      } else {
        const list = h("div", { class: "list" });
        installed.forEach((m) => list.appendChild(installedRow(m, payload, reload)));
        installedBody.appendChild(list);
      }

      // 下载清单
      storeGrid.replaceChildren();
      models.filter((m) => m.tier !== "optin").forEach((m) => {
        storeGrid.appendChild(modelCard(m, reload));
      });
    };

    const reload = async () => {
      const fresh = await get("/api/models").catch(() => null);
      if (fresh) render(fresh);
      await loadSystem().catch(() => {});
    };

    page.querySelector("[data-role=refresh]").addEventListener("click", async () => {
      toast("正在刷新", "", "info", 1200);
      await reload();
    });

    if (data) render(data);
    else {
      installedBody.innerHTML = `<div class="alert alert-danger">读取模型清单失败，请确认服务仍在运行。</div>`;
    }

    // 下载任务进度通过全局事件更新按钮文字。
    const onUpdate = (ev) => {
      const jobs = ev.detail?.jobs || [];
      page.querySelectorAll("[data-download]").forEach((btn) => {
        const id = btn.dataset.download;
        const job = jobs.find((j) => j.type === "download" && j.params?.modelId === id);
        if (!job) return;
        if (job.status === "running" || job.status === "pending") {
          btn.disabled = true;
          btn.textContent = `下载中 ${Math.round((job.overall || 0) * 100)}%`;
        } else if (job.status === "succeeded") {
          btn.textContent = "已下载";
          setTimeout(() => reload(), 800);
        } else {
          btn.disabled = false;
          btn.textContent = "重试下载";
        }
      });
    };
    document.addEventListener("jobs:update", onUpdate);

    root.replaceChildren(page);
    return () => document.removeEventListener("jobs:update", onUpdate);
  },
};

function installedRow(model, payload, reload) {
  const row = h("div", { class: "list-item" });
  const isCurrent = payload.currentModelId && (model.id === payload.currentModelId || model.id.startsWith(payload.currentModelId));
  row.innerHTML = `
    <span class="thumb" style="width:38px;height:38px;border-radius:8px;background:var(--accent-soft);color:var(--accent)">${icon("wave", 16)}</span>
    <div class="grow">
      <div class="title">${escHtml(prettyName(model.id))} ${isCurrent ? `<span class="tag tag-accent" style="margin-left:6px">当前默认</span>` : ""}</div>
      <div class="meta"><span>${escHtml(model.kind)}</span><span>${bytes(model.sizeBytes)}</span><span>${escHtml(model.languages || "")}</span></div>
    </div>
    <div class="cluster" style="gap:4px"></div>
  `;
  const actions = row.querySelector(".cluster");
  const useBtn = h("button", { class: "btn btn-xs btn-soft", type: "button" }, isCurrent ? "已启用" : "设为默认");
  useBtn.disabled = isCurrent;
  useBtn.addEventListener("click", async () => {
    try {
      await post("/api/models/select", { modelId: model.id });
      toast("已设为默认模型", prettyName(model.id), "success");
      await reload();
    } catch (err) {
      toast("设置失败", err.message, "error");
    }
  });
  const remove = h("button", { class: "btn btn-xs btn-danger-soft", type: "button" }, "删除");
  remove.addEventListener("click", async () => {
    const ok = await confirmDialog({
      title: "删除模型文件",
      message: `确定删除「${prettyName(model.id)}」吗？需要时可以重新下载。`,
      confirmText: "删除", danger: true,
    });
    if (!ok) return;
    try {
      const catalogId = guessCatalogId(model.id);
      await post("/api/models/remove", { id: catalogId });
      toast("已删除", "", "success");
      await reload();
    } catch (err) {
      toast("删除失败", err.message, "error");
    }
  });
  actions.append(useBtn, remove);
  return row;
}

function modelCard(model, reload) {
  const card = h("div", { class: "model-card" + (model.installed ? " installed" : "") });
  card.innerHTML = `
    <div class="head">
      <span class="thumb" style="width:40px;height:40px;border-radius:10px;background:var(--surface-2)">${icon("sparkles", 18)}</span>
      <div style="min-width:0">
        <h3>${escHtml(model.name)}${model.recommended ? ` <span class="tag tag-accent">推荐</span>` : ""}</h3>
        <p>${escHtml(model.description)}</p>
      </div>
    </div>
    <div class="facts">
      <span class="tag">${escHtml(model.languages)}</span>
      <span class="tag">${escHtml(model.size)}</span>
      <span class="tag">${escHtml(model.license)}</span>
      ${model.installed ? `<span class="tag tag-success">已安装 ${model.installedSize ? bytes(model.installedSize) : ""}</span>` : ""}
    </div>
    <div class="actions"></div>
  `;
  const actions = card.querySelector(".actions");
  if (model.installed) {
    actions.appendChild(h("span", { class: "small muted" }, "已就绪"));
    if (model.recommended) {
      const use = h("button", { class: "btn btn-xs btn-ghost", type: "button" }, "设为默认");
      use.addEventListener("click", async () => {
        try {
          const installed = await get("/api/models");
          const match = (installed.installed || []).find((m) => m.id.includes(model.id) || m.id.startsWith(model.id));
          await post("/api/models/select", { modelId: match?.id || model.id });
          toast("已设为默认模型", model.name, "success");
          await reload();
        } catch (err) {
          toast("设置失败", err.message, "error");
        }
      });
      actions.appendChild(use);
    }
  } else {
    const btn = h("button", { class: "btn btn-sm btn-primary", type: "button", "data-download": model.id }, "下载");
    btn.addEventListener("click", async () => {
      btn.disabled = true;
      btn.textContent = "准备下载…";
      try {
        await post("/api/models/download", { id: model.id });
        toast("开始下载", `${model.name}（${model.size}）`, "info");
      } catch (err) {
        btn.disabled = false;
        btn.textContent = "下载";
        toast("下载失败", err.message, "error");
      }
    });
    actions.appendChild(btn);
  }
  return card;
}

function prettyName(id) {
  if (/sense-voice/i.test(id)) return "SenseVoice";
  if (/paraformer/i.test(id)) return "Paraformer 中文";
  if (/reazon/i.test(id)) return "ReazonSpeech";
  if (/whisper/i.test(id)) return "Whisper";
  if (/parakeet/i.test(id)) return "Parakeet";
  if (/omnilingual/i.test(id)) return "Omnilingual";
  return id;
}

function guessCatalogId(dirName) {
  if (/sense-voice/i.test(dirName)) return "sense-voice";
  if (/paraformer-zh/i.test(dirName)) return "paraformer-zh";
  if (/reazon/i.test(dirName)) return "reazonspeech";
  if (/whisper-tiny/i.test(dirName)) return "whisper-tiny";
  if (/silero/i.test(dirName)) return "silero-vad";
  if (/parakeet.*v3/i.test(dirName)) return "parakeet-v3";
  if (/parakeet.*v2/i.test(dirName)) return "parakeet-v2";
  if (/omnilingual/i.test(dirName)) return "omnilingual";
  return dirName;
}
