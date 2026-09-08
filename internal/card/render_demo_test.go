package card

// 辅助：把样例卡片渲染到项目 tmp 目录供人工查看（go test -run TestRenderDemo）。

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRenderDemo(t *testing.T) {
	if os.Getenv("CARD_RENDER_DEMO") == "" {
		t.Skip("设置 CARD_RENDER_DEMO=1 以输出样例卡片到 tmp/（go test ./internal/card/ -run TestRenderDemo -env CARD_RENDER_DEMO=1）")
	}
	g, st, dir := newTestGen(t)
	seedVideo(t, st, dir)
	path, err := g.RenderToFile(context.Background(), "BV1TestCard01")
	if err != nil {
		t.Fatal(err)
	}
	out, _ := filepath.Abs(filepath.Join("..", "..", "tmp"))
	_ = os.MkdirAll(out, 0o755)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "card-demo.png"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Log("written to tmp/card-demo.png")
}
