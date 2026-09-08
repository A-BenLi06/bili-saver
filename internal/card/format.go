package card

// 数值/时间格式化（中文社区习惯：万/亿缩放）。

import (
	"fmt"
	"math"
	"time"
)

// formatNumber 12345 -> "1.2万"，120000000 -> "1.20亿"。
func formatNumber(n int) string {
	if n < 0 {
		n = 0
	}
	switch {
	case n >= 100_000_000:
		v := float64(n) / 100_000_000
		return trimFloat(v, 2) + "亿"
	case n >= 10_000:
		v := float64(n) / 10_000
		return trimFloat(v, 1) + "万"
	default:
		return fmt.Sprintf("%d", n)
	}
}

func trimFloat(v float64, prec int) string {
	s := fmt.Sprintf("%.*f", prec, v)
	if prec > 0 {
		s = stringTrimRightZeros(s)
	}
	return s
}

func stringTrimRightZeros(s string) string {
	for len(s) > 0 && s[len(s)-1] == '0' {
		s = s[:len(s)-1]
	}
	if len(s) > 0 && s[len(s)-1] == '.' {
		s = s[:len(s)-1]
	}
	return s
}

// formatDuration 秒 -> "mm:ss" / "h:mm:ss"。
func formatDuration(sec int) string {
	if sec < 0 {
		sec = 0
	}
	h := sec / 3600
	m := (sec % 3600) / 60
	s := sec % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", m, s)
}

// formatShortDate 2006-01-02。
func formatShortDate(ts int64) string {
	if ts <= 0 {
		return "—"
	}
	return time.Unix(ts, 0).Format("2006-01-02")
}

// formatFullDate 2006-01-02 15:04。
func formatFullDate(ts int64) string {
	if ts <= 0 {
		return "—"
	}
	return time.Unix(ts, 0).Format("2006-01-02 15:04")
}

// relativeTime 相对时间：x秒前 / x分钟前 / x小时前 / x天前 / x个月前 / x年前。
func relativeTime(ts, now int64) string {
	if ts <= 0 {
		return "—"
	}
	d := now - ts
	if d < 0 {
		d = 0
	}
	switch {
	case d < 60:
		return "刚刚"
	case d < 3600:
		return fmt.Sprintf("%d分钟前", d/60)
	case d < 86400:
		return fmt.Sprintf("%d小时前", d/3600)
	case d < 86400*30:
		return fmt.Sprintf("%d天前", d/86400)
	case d < 86400*365:
		return fmt.Sprintf("%d个月前", d/(86400*30))
	default:
		return fmt.Sprintf("%d年前", int(math.Round(float64(d)/86400/365)))
	}
}
