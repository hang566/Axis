#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""sherpa-onnx 常驻识别服务（供「视听工作台」本地引擎调用）。

与一次性脚本不同，这个进程常驻：模型只加载一次，之后每段音频直接复用，
长音频的识别进度也能实时回传。通信方式是 stdin/stdout 各一行 JSON。

请求：
  {"type":"transcribe","id":"r1","audio":"a.wav","language":"zh","model":""}
响应：
  {"type":"progress","id":"r1","pct":0.42,"stage":"识别中 3/12"}
  {"type":"result","id":"r1","segments":[{"start":0.1,"end":1.2,"text":"..."}],
   "language":"zh","model":"sense-voice"}
  {"type":"error","id":"r1","message":"..."}
启动完成时先输出一行：
  {"type":"ready","model":"sense-voice"}

依赖：pip install sherpa_onnx
"""
import argparse
import json
import os
import sys
import wave

# 单条片段的软上限：超过则按标点/字数/时段再切分（字幕层还会再合并与折行）。
MAX_SEG_CHARS = 36
MAX_SEG_SECONDS = 6.0
MAX_SEG_CHARS_HARD = 80

# VAD 参数：静音 0.5s 视为断句；单段最长 20s。
VAD_THRESHOLD = 0.5
VAD_MIN_SILENCE = 0.5
VAD_MIN_SPEECH = 0.3
VAD_MAX_SPEECH = 20.0
VAD_WINDOW = 512

# 相邻语音段间隔小于该值则合并，避免碎片化。
MERGE_GAP = 0.6
MERGE_MAX_SECONDS = 20.0

# 识别时向 VAD 边界外扩的时长，避免切掉字头字尾。
SEGMENT_PAD = 0.25

# 分词断句：词间停顿超过该秒数视为断句点。
TOKEN_GAP = 0.45
SOFT_BREAK_MIN_CHARS = 6
HARD_BREAK_PUNCT = "。！？!?；;"
SOFT_BREAK_PUNCT = "，,、：:"
JOIN_PUNCT = "。！？!?；;，,、：:）)】》”’"

SENSE_VOICE_LANGS = {"zh", "en", "ja", "ko", "yue"}


def log(msg):
    """日志一律走 stderr，stdout 只留给协议消息。"""
    print(msg, file=sys.stderr, flush=True)


def emit(obj):
    sys.stdout.write(json.dumps(obj, ensure_ascii=False) + "\n")
    sys.stdout.flush()


def read_wav(path):
    """读取 16bit 单声道 WAV，返回 (float 样本列表, 采样率)。"""
    import array

    with wave.open(path, "rb") as w:
        channels = w.getnchannels()
        width = w.getsampwidth()
        sr = w.getframerate()
        frames = w.readframes(w.getnframes())
    if width != 2:
        raise ValueError(f"需要 16bit PCM WAV，实际 {width * 8}bit")
    pcm = array.array("h")
    pcm.frombytes(frames)
    if sys.byteorder == "big":
        pcm.byteswap()
    samples = [v / 32768.0 for v in pcm]
    if channels == 2:
        samples = samples[0::2]
    elif channels != 1:
        raise ValueError(f"需要单声道，实际 {channels} 声道")
    return samples, sr


# ---------- 模型发现 ----------

def detect_kind(directory):
    """按目录内文件名判断模型族。"""
    names = os.listdir(directory)
    lower = [n.lower() for n in names]
    if any(n.startswith("encoder") and n.endswith(".onnx") for n in lower):
        return "transducer"
    if any(n.startswith("model") and n.endswith(".onnx") for n in lower):
        if "sense" in os.path.basename(directory).lower():
            return "sense_voice"
        return "paraformer"
    onnx = [n for n in lower if n.endswith(".onnx")]
    if onnx:
        return "paraformer"
    return ""


def pick_onnx(directory, prefixes):
    """按前缀优先选 int8 版本。"""
    names = sorted(os.listdir(directory))
    for want_int8 in (True, False):
        for prefix in prefixes:
            for name in names:
                low = name.lower()
                if not low.startswith(prefix) or not low.endswith(".onnx"):
                    continue
                if want_int8 and "int8" not in low:
                    continue
                if not want_int8 and "int8" in low:
                    continue
                return os.path.join(directory, name)
    return ""


def discover_models(models_dir):
    """扫描 models/ 下所有可用的 ASR 模型，返回 [{id, kind, path}]。"""
    out = []
    if not os.path.isdir(models_dir):
        return out
    for name in sorted(os.listdir(models_dir)):
        full = os.path.join(models_dir, name)
        if not os.path.isdir(full):
            continue
        kind = detect_kind(full)
        if not kind:
            continue
        if not os.path.isfile(os.path.join(full, "tokens.txt")):
            continue
        out.append({"id": name, "kind": kind, "path": full})
    return out


def choose_model(models, requested, language):
    if not models:
        raise RuntimeError(
            "没有可用的语音模型。请打开「模型中心」下载一个模型"
            "（中文推荐 SenseVoice 或 Paraformer），或改用在线识别接口。"
        )
    if requested:
        for m in models:
            if requested == m["id"] or requested.startswith(m["id"]) or m["id"].startswith(requested):
                return m
        raise RuntimeError(f"找不到模型 {requested}，可用：" + "、".join(m["id"] for m in models))

    lang = (language or "").lower()
    # 多语种优先；其次按语言匹配。
    for m in models:
        if m["kind"] == "sense_voice":
            return m
    if lang in ("ja", "en"):
        for m in models:
            if m["kind"] == "transducer":
                return m
    for m in models:
        if "paraformer" in m["id"].lower():
            return m
    return models[0]


class Engine:
    def __init__(self, models_dir, model_id, threads):
        self.models_dir = models_dir
        self.requested = model_id
        self.threads = max(1, threads)
        self.models = discover_models(models_dir)
        self._recognizer = None
        self._recognizer_key = None
        self._vad = None

    # ---- 识别器 ----

    def recognizer(self, language):
        model = choose_model(self.models, self.requested, language)
        lang = (language or "").lower()
        key = (model["id"], lang if model["kind"] == "sense_voice" and lang in SENSE_VOICE_LANGS else "")
        if self._recognizer is not None and self._recognizer_key == key:
            return self._recognizer, model
        import sherpa_onnx

        path = model["path"]
        tokens = os.path.join(path, "tokens.txt")
        kind = model["kind"]
        log(f"加载模型 {model['id']}（{kind}）…")
        if kind == "sense_voice":
            rec = sherpa_onnx.OfflineRecognizer.from_sense_voice(
                model=pick_onnx(path, ["model.int8", "model"]),
                tokens=tokens,
                num_threads=self.threads,
                sample_rate=16000,
                feature_dim=80,
                use_itn=True,
                language=lang if lang in SENSE_VOICE_LANGS else "",
            )
        elif kind == "transducer":
            rec = sherpa_onnx.OfflineRecognizer.from_transducer(
                encoder=pick_onnx(path, ["encoder.int8", "encoder"]),
                decoder=pick_onnx(path, ["decoder.int8", "decoder"]),
                joiner=pick_onnx(path, ["joiner.int8", "joiner"]),
                tokens=tokens,
                num_threads=self.threads,
                sample_rate=16000,
                feature_dim=80,
            )
        else:
            rec = sherpa_onnx.OfflineRecognizer.from_paraformer(
                paraformer=pick_onnx(path, ["model.int8", "model"]),
                tokens=tokens,
                num_threads=self.threads,
                sample_rate=16000,
                feature_dim=80,
            )
        self._recognizer = rec
        self._recognizer_key = key
        log(f"模型就绪：{model['id']}")
        return rec, model

    def vad(self, sample_rate):
        if self._vad is not None:
            return self._vad
        import sherpa_onnx

        path = os.path.join(self.models_dir, "silero_vad.onnx")
        if not os.path.isfile(path):
            return None
        config = sherpa_onnx.VadModelConfig()
        config.silero_vad.model = path
        config.silero_vad.threshold = VAD_THRESHOLD
        config.silero_vad.min_silence_duration = VAD_MIN_SILENCE
        config.silero_vad.min_speech_duration = VAD_MIN_SPEECH
        config.silero_vad.max_speech_duration = VAD_MAX_SPEECH
        config.silero_vad.window_size = VAD_WINDOW
        config.sample_rate = sample_rate
        self._vad = sherpa_onnx.VoiceActivityDetector(config, buffer_size_in_seconds=60)
        return self._vad

    def speech_ranges(self, samples, sample_rate, progress):
        total = len(samples) / float(sample_rate)
        vad = self.vad(sample_rate)
        if vad is None:
            return [(0.0, total)]
        ranges = []
        step = max(1, len(samples) // 100)
        for i in range(0, len(samples), VAD_WINDOW):
            chunk = samples[i:i + VAD_WINDOW]
            if len(chunk) < VAD_WINDOW:
                chunk = chunk + [0.0] * (VAD_WINDOW - len(chunk))
            vad.accept_waveform(chunk)
            while not vad.empty():
                seg = vad.front
                start = seg.start / sample_rate
                ranges.append((start, start + len(seg.samples) / float(sample_rate)))
                vad.pop()
            if i % step == 0 and progress:
                progress(0.05 + 0.10 * min(1.0, i / float(max(1, len(samples)))), "检测语音段")
        vad.flush()
        while not vad.empty():
            seg = vad.front
            start = seg.start / sample_rate
            ranges.append((start, start + len(seg.samples) / float(sample_rate)))
            vad.pop()
        if not ranges:
            return []
        merged = [list(ranges[0])]
        for start, end in ranges[1:]:
            if start - merged[-1][1] <= MERGE_GAP and end - merged[-1][0] <= MERGE_MAX_SECONDS:
                merged[-1][1] = end
            else:
                merged.append([start, end])
        return [(s, e) for s, e in merged]

    def decode(self, recognizer, samples, sample_rate):
        stream = recognizer.create_stream()
        stream.accept_waveform(sample_rate, samples)
        recognizer.decode_stream(stream)
        return stream.result


def slice_padded(samples, sample_rate, start, end):
    pad = int(SEGMENT_PAD * sample_rate)
    begin = max(0, int(start * sample_rate) - pad)
    finish = min(len(samples), int(end * sample_rate) + pad)
    return samples[begin:finish], begin / float(sample_rate)


def join_tokens(tokens):
    out = []
    for token in tokens:
        if token.startswith("▁"):
            out.append(" " + token[1:])
        else:
            out.append(token)
    return "".join(out)


def split_by_tokens(result, start, end, audio_start):
    """优先用分词时间戳断句；缺时间戳时按标点/字数切分。"""
    text = (result.text or "").strip()
    if not text:
        return []
    tokens = list(getattr(result, "tokens", None) or [])
    stamps = list(getattr(result, "timestamps", None) or [])
    if not tokens or len(tokens) != len(stamps):
        return split_text(text, start, end)

    # 每个 token 的起始字符偏移，用于统计「本段已累积多少字」。
    offsets = []
    cursor = 0
    for t in tokens:
        offsets.append(cursor)
        cursor += len(t)

    cuts = [0]
    for i in range(1, len(tokens)):
        prev = tokens[i - 1]
        chars_in_cut = offsets[i] - offsets[cuts[-1]]  # 到当前 token 之前本段的字数
        ending = prev[-1:] in HARD_BREAK_PUNCT
        clause = prev[-1:] in SOFT_BREAK_PUNCT and chars_in_cut >= SOFT_BREAK_MIN_CHARS
        gap = stamps[i] - stamps[i - 1] > TOKEN_GAP
        too_long = chars_in_cut >= MAX_SEG_CHARS
        too_slow = stamps[i] - stamps[cuts[-1]] >= MAX_SEG_SECONDS
        if ending or clause or gap or too_long or too_slow:
            cuts.append(i)
    if cuts[-1] != len(tokens):
        cuts.append(len(tokens))

    segments = []
    for idx in range(len(cuts) - 1):
        first, last = cuts[idx], cuts[idx + 1]
        piece = join_tokens(tokens[first:last]).strip()
        if not piece:
            continue
        seg_start = max(start, audio_start + stamps[first])
        if idx + 2 < len(cuts):
            seg_end = min(end, audio_start + stamps[cuts[idx + 1]])
        else:
            seg_end = min(end, audio_start + stamps[last - 1] + 0.35)
        segments.append([seg_start, max(seg_end, seg_start + 0.05), piece])
    return merge_punct_only(segments)


def merge_punct_only(segments):
    merged = []
    for seg in segments:
        only_punct = all(ch in HARD_BREAK_PUNCT + SOFT_BREAK_PUNCT + " " for ch in seg[2])
        if only_punct and merged:
            merged[-1][1] = max(merged[-1][1], seg[1])
            merged[-1][2] += seg[2]
        elif not only_punct:
            merged.append(seg)
    return merged


def split_text(text, start, end):
    """无时间戳时按标点与长度切分，并按字数比例分配时间。"""
    text = text.strip()
    if not text:
        return []
    pieces = []
    for sentence in split_keep(text, HARD_BREAK_PUNCT):
        for clause in split_keep(sentence, SOFT_BREAK_PUNCT):
            while len(clause) > MAX_SEG_CHARS:
                pieces.append(clause[:MAX_SEG_CHARS])
                clause = clause[MAX_SEG_CHARS:]
            if clause.strip():
                pieces.append(clause.strip())
    merged = []
    for piece in pieces:
        if merged and len(piece) <= 4 and len(merged[-1]) + len(piece) <= MAX_SEG_CHARS + 8:
            merged[-1] += piece
        else:
            merged.append(piece)
    pieces = merged
    if len(pieces) <= 1:
        return [[start, end, text]]
    total = sum(max(1, len(p)) for p in pieces)
    span = max(0.1, end - start)
    out = []
    cursor = start
    for idx, piece in enumerate(pieces):
        share = span * max(1, len(piece)) / total
        seg_end = end if idx == len(pieces) - 1 else cursor + share
        out.append([cursor, seg_end, piece])
        cursor = seg_end
    return out


def split_keep(text, puncts):
    out, cur = [], ""
    for ch in text:
        cur += ch
        if ch in puncts:
            if cur.strip():
                out.append(cur.strip())
            cur = ""
    if cur.strip():
        out.append(cur.strip())
    return out


def lang_code(result):
    raw = (getattr(result, "lang", "") or "").strip()
    if raw.startswith("<|") and raw.endswith("|>"):
        raw = raw[2:-2]
    return raw.lower()


def transcribe(engine, audio_path, language, progress):
    samples, sr = read_wav(audio_path)
    if not samples:
        raise RuntimeError("音频为空")
    progress(0.02, "准备音频")
    ranges = engine.speech_ranges(samples, sr, progress)
    if not ranges:
        return [], {"language": language or "", "model": ""}

    recognizer, model = engine.recognizer(language)
    requested = (language or "").lower()
    detected = ""

    # SenseVoice 的语种自动检测按段进行，短片段容易误判；
    # 先在最长的一段上探一次，再把结果固定给全部片段。
    if model["kind"] == "sense_voice" and not requested:
        probe = max(range(len(ranges)), key=lambda i: ranges[i][1] - ranges[i][0])
        part, _ = slice_padded(samples, sr, *ranges[probe])
        if part:
            result = engine.decode(recognizer, part, sr)
            detected = lang_code(result)
            if detected in SENSE_VOICE_LANGS:
                recognizer, model = engine.recognizer(detected)

    segments = []
    total = max(1, len(ranges))
    for index, (start, end) in enumerate(ranges):
        part, audio_start = slice_padded(samples, sr, start, end)
        if part:
            result = engine.decode(recognizer, part, sr)
            segments.extend(split_by_tokens(result, start, end, audio_start))
            if not detected:
                detected = lang_code(result)
        progress(0.15 + 0.80 * (index + 1) / total, f"识别中 {index + 1}/{total}")
    return segments, {"language": detected or requested, "model": model["id"]}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--models-dir", default="models")
    parser.add_argument("--model", default="")
    parser.add_argument("--threads", type=int, default=2)
    args = parser.parse_args()

    engine = Engine(args.models_dir, args.model, args.threads)
    # 提前加载默认模型，尽早暴露环境问题（失败也继续，等请求时报错）。
    try:
        engine.recognizer("")
    except Exception as exc:  # noqa: BLE001
        log(f"预热模型失败：{exc}")
    emit({"type": "ready", "model": args.model or "auto"})

    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            req = json.loads(line)
        except Exception:  # noqa: BLE001
            continue
        rid = req.get("id", "")
        if req.get("type") != "transcribe":
            continue

        def progress(pct, stage, _rid=rid):
            emit({"type": "progress", "id": _rid, "pct": round(float(pct), 4), "stage": stage})

        try:
            segments, meta = transcribe(
                engine,
                req.get("audio", ""),
                req.get("language", "") or "",
                progress,
            )
            emit({
                "type": "result",
                "id": rid,
                "segments": [
                    {"start": round(s, 3), "end": round(e, 3), "text": t}
                    for s, e, t in segments
                ],
                "language": meta.get("language", ""),
                "model": meta.get("model", ""),
            })
        except Exception as exc:  # noqa: BLE001
            log(f"识别失败：{exc}")
            emit({"type": "error", "id": rid, "message": str(exc)})


if __name__ == "__main__":
    main()
