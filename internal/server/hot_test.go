package server

// 热门视频二次确认的阈值判定测试。

import (
	"strings"
	"testing"

	"bili-saver/internal/config"
)

func TestHotReason(t *testing.T) {
	s := &Server{Deps: Deps{Cfg: config.Defaults()}} // 默认 100万播放 / 1万评论

	if got := s.hotReason(999999, 9999); got != "" {
		t.Errorf("阈值内不应提示: %q", got)
	}
	if got := s.hotReason(1_000_000, 0); !strings.Contains(got, "播放 100.0万") {
		t.Errorf("播放达阈值应提示: %q", got)
	}
	if got := s.hotReason(0, 10000); !strings.Contains(got, "评论 1.0万") {
		t.Errorf("评论达阈值应提示: %q", got)
	}
	got := s.hotReason(2_500_000, 23000)
	if !strings.Contains(got, "250.0万") || !strings.Contains(got, "2.3万") {
		t.Errorf("双阈值文案错误: %q", got)
	}

	// 阈值禁用（0）时不提示
	s.Deps.Cfg.Collector.HotVideoViews = 0
	s.Deps.Cfg.Collector.HotVideoReplies = 0
	if got := s.hotReason(99999999, 999999); got != "" {
		t.Errorf("禁用后不应提示: %q", got)
	}
}
