// card-demo 用真实 data/ 数据渲染卡片到 tmp/ 供人工比对（临时工具）。
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"bili-saver/internal/card"
	"bili-saver/internal/config"
	"bili-saver/internal/store"
)

func main() {
	dir, _ := os.Getwd()
	cfg := config.Defaults()
	cfg.DataDir = filepath.Join(dir, "data")
	st, err := store.Open(filepath.Join(cfg.DataDir, "bili-saver.db"))
	if err != nil {
		panic(err)
	}
	defer st.Close()
	g := card.New(st, cfg, slog.Default())
	vids, err := st.ListVideos(0, "ok", 2, 0)
	if err != nil {
		panic(err)
	}
	for _, v := range vids {
		path, err := g.RenderToFile(context.Background(), v.Bvid)
		if err != nil {
			fmt.Println("FAIL", v.Bvid, err)
			continue
		}
		fmt.Println("OK", v.Bvid, path)
	}
}
