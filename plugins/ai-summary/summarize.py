#!/usr/bin/env python3
"""ai-summary 插件：video.downloaded → 读字幕/简介 → LLM 总结 → save_summary。"""
import json
import os
import sys
import urllib.request


def chat(cfg: dict, prompt: str) -> str | None:
    base = (cfg.get("api_base") or "").rstrip("/")
    if not base:
        return None
    body = json.dumps({
        "model": cfg.get("model") or "qwen2.5:7b",
        "messages": [{"role": "user", "content": prompt}],
    }).encode()
    req = urllib.request.Request(base + "/chat/completions", data=body,
                                 headers={"Content-Type": "application/json"})
    if cfg.get("api_key"):
        req.add_header("Authorization", "Bearer " + cfg["api_key"])
    try:
        with urllib.request.urlopen(req, timeout=300) as r:
            data = json.loads(r.read())
            return data["choices"][0]["message"]["content"]
    except Exception as e:
        sys.stderr.write(f"LLM 调用失败: {e}\n")
        return None


def collect_context(data_dir: str, bvid: str, max_chars: int) -> str:
    """收集字幕文本与官方 AI 小结（origin 数据），截断到 max_chars。"""
    parts = []
    # 字幕（优先 B 站 CC，其次 AI 转录）
    media_root = os.path.join(data_dir, "media", bvid)
    subs = []
    if os.path.isdir(media_root):
        for dirpath, _, files in os.walk(media_root):
            for f in files:
                if f.endswith(".bcc.json"):
                    subs.append((0, os.path.join(dirpath, f)))
                elif f.endswith(".srt"):
                    subs.append((1, os.path.join(dirpath, f)))
    subs.sort()
    for _, path in subs[:2]:
        try:
            if path.endswith(".bcc.json"):
                with open(path, encoding="utf-8") as fh:
                    body = json.load(fh).get("body", [])
                text = "".join(x.get("content", "") for x in body)
            else:
                with open(path, encoding="utf-8") as fh:
                    text = "".join(line for line in fh if not line.strip().isdigit()
                                   and "-->" not in line)
            if text.strip():
                parts.append(text[:max_chars])
                break
        except Exception:
            continue
    return "\n".join(parts)[:max_chars]


def main():
    ev = json.loads(sys.stdin.readline())
    cfg = ev.get("config") or {}
    ctx = ev.get("context") or {}
    bvid = (ev.get("payload") or {}).get("bvid")
    if not bvid:
        return
    text = collect_context(ctx.get("data_dir", ""), bvid, int(cfg.get("max_chars") or 6000))
    if not text:
        print(json.dumps({"op": "log", "params": {"msg": "无字幕/简介可总结: " + bvid}}))
        return
    summary = chat(cfg, f"请用中文把以下视频内容总结成 5 句以内的要点摘要：\n\n{text}")
    if not summary:
        return
    print(json.dumps({
        "op": "save_summary",
        "params": {"bvid": bvid, "content": summary,
                   "model": cfg.get("model"), "lang": "zh"},
    }, ensure_ascii=False))


if __name__ == "__main__":
    main()
