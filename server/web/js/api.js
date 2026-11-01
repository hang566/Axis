// 后端接口封装：统一错误处理、JSON 解析与文件上传进度。

export class ApiError extends Error {
  constructor(message, status, hint) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.hint = hint || "";
  }
}

async function readError(res) {
  try {
    const data = await res.json();
    if (data && data.error) return new ApiError(data.error, res.status, data.hint);
  } catch (_) {
    /* 非 JSON 响应 */
  }
  return new ApiError(`请求失败（HTTP ${res.status}）`, res.status);
}

export async function get(path) {
  const res = await fetch(path, { headers: { Accept: "application/json" } });
  if (!res.ok) throw await readError(res);
  return res.json();
}

export async function send(method, path, body) {
  const res = await fetch(path, {
    method,
    headers: { "Content-Type": "application/json", Accept: "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) throw await readError(res);
  if (res.status === 204) return {};
  return res.json();
}

export const post = (path, body) => send("POST", path, body);
export const patch = (path, body) => send("PATCH", path, body);
export const del = (path) => send("DELETE", path, {});

/** 上传文件，onProgress(loaded, total) 汇报进度；支持 AbortSignal。 */
export function upload(path, file, fields = {}, { onProgress, signal } = {}) {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    const form = new FormData();
    form.append("file", file, file.name);
    for (const [key, value] of Object.entries(fields)) {
      if (value !== undefined && value !== null && String(value) !== "") {
        form.append(key, String(value));
      }
    }
    xhr.open("POST", path, true);
    xhr.responseType = "json";
    if (onProgress) {
      xhr.upload.onprogress = (ev) => {
        if (ev.lengthComputable) onProgress(ev.loaded, ev.total);
      };
    }
    xhr.onload = () => {
      let payload = xhr.response;
      if (!payload && xhr.responseText) {
        try { payload = JSON.parse(xhr.responseText); } catch (_) { payload = null; }
      }
      if (xhr.status >= 200 && xhr.status < 300) {
        resolve(payload || {});
      } else {
        reject(new ApiError(payload?.error || `上传失败（HTTP ${xhr.status}）`, xhr.status, payload?.hint));
      }
    };
    xhr.onerror = () => reject(new ApiError("网络中断，上传失败", 0, "请确认服务仍在运行"));
    xhr.onabort = () => reject(new ApiError("已取消上传", 0));
    if (signal) {
      signal.addEventListener("abort", () => xhr.abort(), { once: true });
    }
    xhr.send(form);
  });
}

/** 打开 SSE：任务状态变化时回调。断线自动重连。 */
export function subscribeJobs(onUpdate) {
  let source = null;
  let closed = false;
  const connect = () => {
    if (closed) return;
    source = new EventSource("/api/events");
    source.addEventListener("snapshot", (ev) => onUpdate(safeParse(ev.data)));
    source.addEventListener("jobs", (ev) => onUpdate(safeParse(ev.data)));
    source.onerror = () => {
      source?.close();
      if (!closed) setTimeout(connect, 2500);
    };
  };
  connect();
  return () => { closed = true; source?.close(); };
}

function safeParse(text) {
  try { return JSON.parse(text); } catch (_) { return null; }
}

/** 触发浏览器下载（服务端文件地址）。 */
export function download(url, filename) {
  const link = document.createElement("a");
  link.href = url;
  if (filename) link.download = filename;
  document.body.appendChild(link);
  link.click();
  link.remove();
}
