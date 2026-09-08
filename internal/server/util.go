package server

// 小工具：二维码 PNG base64。

import (
	"encoding/base64"

	"github.com/skip2/go-qrcode"
)

func qrPNG(url string) (string, error) {
	b, err := qrcode.Encode(url, qrcode.Medium, 320)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}
