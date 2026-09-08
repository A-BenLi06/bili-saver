#!/usr/bin/env python3
"""ai-search 插件：video.downloaded → 生成标题+简介的向量 → save_embedding。"""
import json
import sys
import urllib.request


def embed(cfg: dict, text: str) -> list | None:
    base = (cfg.get("api_base") or "").rstrip("/")
    if not base:
        return None
    body = json.dumps({"model": cfg.get("model") or "bge-m3", "input": text}).encode()
    req = urllib.request.Request(base + "/embeddings", data=body,
                                 headers={"Content-Type": "application/json"})
    if cfg.get("api_key"):
        req.add_header("Authorization", "Bearer " + cfg["api_key"])
    try:
        with urllib.request.urlopen(req, timeout=120) as r:
            data = json.loads(r.read())
            return data["data"][0]["embedding"]
    except Exception as e:
        sys.stderr.write(f"embedding 失败: {e}\n")
        return None


def main():
    ev = json.loads(sys.stdin.readline())
    cfg = ev.get("config") or {}
    payload = ev.get("payload") or {}
    bvid = payload.get("bvid")
    if not bvid:
        return
    text = " ".join(str(v) for v in payload.values() if isinstance(v, str))
    if not text:
        return
    vec = embed(cfg, text)
    if not vec:
        return
    print(json.dumps({
        "op": "save_embedding",
        "params": {"entity_type": "video", "entity_id": bvid,
                   "model": cfg.get("model"), "vec": vec},
    }))


if __name__ == "__main__":
    main()
