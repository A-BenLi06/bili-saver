#!/usr/bin/env python3
"""ai-tagger 插件：video.downloaded → LLM 生成补充标签 → save_tag。"""
import json
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
        with urllib.request.urlopen(req, timeout=180) as r:
            return json.loads(r.read())["choices"][0]["message"]["content"]
    except Exception as e:
        sys.stderr.write(f"LLM 调用失败: {e}\n")
        return None


def main():
    ev = json.loads(sys.stdin.readline())
    cfg = ev.get("config") or {}
    bvid = (ev.get("payload") or {}).get("bvid")
    if not bvid:
        return
    out = chat(cfg, "为一条视频生成 3-5 个补充标签，只输出逗号分隔的标签本身，不要其他文字。\n"
                    "标题：随便（测试占位，实际事件只带 bvid）")
    if not out:
        return
    tags = [t.strip() for t in out.replace("，", ",").split(",") if t.strip()][:5]
    if tags:
        print(json.dumps({"op": "save_tag", "params": {"bvid": bvid, "tags": tags}},
                         ensure_ascii=False))


if __name__ == "__main__":
    main()
