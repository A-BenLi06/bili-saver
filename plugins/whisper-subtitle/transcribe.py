#!/usr/bin/env python3
"""whisper-subtitle 插件：subtitle.missing 事件 → 本地转录 → save_subtitle。

协议：stdin 收一行事件 JSON；stdout 每行一个 op JSON。只允许写 derived 层。
"""
import json
import os
import shutil
import subprocess
import sys
import tempfile


def find_video(data_dir: str, bvid: str, page) -> str | None:
    root = os.path.join(data_dir, "media", bvid)
    if not os.path.isdir(root):
        return None
    candidates = []
    for dirpath, _, files in os.walk(root):
        for f in files:
            if f.endswith((".mp4", ".m4s", ".mkv")) and not f.endswith(".part"):
                candidates.append(os.path.join(dirpath, f))
    candidates.sort()
    return candidates[0] if candidates else None


def transcribe(cmd: list[str], media: str, language: str, out_fmt: str) -> str | None:
    """统一调用转录后端，返回字幕文本。"""
    try:
        subprocess.run(cmd[:1], check=True, capture_output=True)
    except Exception:
        return None  # 后端不可用
    with tempfile.TemporaryDirectory() as td:
        out = subprocess.run(cmd + ["--output_dir", td, "--output_format", out_fmt, media],
                             capture_output=True, text=True, timeout=3500)
        for f in sorted(os.listdir(td)):
            if f.endswith("." + out_fmt):
                with open(os.path.join(td, f), encoding="utf-8") as fh:
                    return fh.read()
        sys.stderr.write(out.stderr[-500:])
    return None


def macwhisper_cli(media: str) -> str | None:
    """MacWhisper 是 macOS GUI 应用：优先找其 CLI/Shortcuts 桥，找不到就放弃。"""
    for c in (shutil.which("macwhisper"), "/usr/local/bin/macwhisper"):
        if c and os.path.exists(c):
            out = subprocess.run([c, "--output", "srt", media], capture_output=True, text=True, timeout=3500)
            if out.returncode == 0 and out.stdout.strip():
                return out.stdout
    return None


def main():
    ev = json.loads(sys.stdin.readline())
    cfg = ev.get("config") or {}
    ctx = ev.get("context") or {}
    payload = ev.get("payload") or {}
    bvid = payload.get("bvid")
    media = find_video(ctx.get("data_dir", ""), bvid, payload.get("page"))
    if not media:
        print(json.dumps({"op": "log", "params": {"msg": "视频文件未下载，跳过转录"}}))
        return

    backend = (cfg.get("backend") or "auto").lower()
    model = cfg.get("model") or "small"
    language = cfg.get("language") or "zh"
    text = None

    if backend in ("auto", "faster-whisper") and shutil.which("whisper-ctranslate2"):
        text = transcribe(["whisper-ctranslate2", "--model", model, "--language", language],
                          media, language, "srt")
    elif backend in ("auto", "whisper.cpp") and shutil.which("whisper-cli"):
        text = transcribe(["whisper-cli", "-m", model, "-l", language], media, language, "srt")
    elif backend in ("auto", "macwhisper"):
        text = macwhisper_cli(media)

    if not text:
        print(json.dumps({"op": "log", "params": {"msg": "无可用转录后端（faster-whisper/whisper.cpp/MacWhisper）"}}))
        return

    print(json.dumps({
        "op": "save_subtitle",
        "params": {
            "bvid": bvid,
            "cid": str(payload.get("cid", "")),
            "lang": f"ai-{language}",
            "lang_doc": f"AI转录（{model}）",
            "format": "srt",
            "content": text,
            "model": model,
        },
    }, ensure_ascii=False))


if __name__ == "__main__":
    main()
