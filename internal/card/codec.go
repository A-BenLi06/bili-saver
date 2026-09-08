package card

// 图片编解码与文件小工具。

import (
	"bytes"
	"image"
	"image/jpeg"
	"image/png"
	"os"
)

func decodeBytes(b []byte) image.Image {
	im, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return nil
	}
	return im
}

func decodeFile(path string) image.Image {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	im, _, err := image.Decode(f)
	if err != nil {
		return nil
	}
	return im
}

func encodePNG(f *os.File, im image.Image) error {
	return png.Encode(f, im)
}

func encodeJPEGFile(path string, im image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return jpeg.Encode(f, im, &jpeg.Options{Quality: 85})
}
