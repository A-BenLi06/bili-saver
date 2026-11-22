package card

// 图片编解码与文件小工具。

import (
	"bytes"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"sync"
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

type pngBufferPool struct {
	pool sync.Pool
}

func (p *pngBufferPool) Get() *png.EncoderBuffer {
	buffer, _ := p.pool.Get().(*png.EncoderBuffer)
	return buffer
}

func (p *pngBufferPool) Put(buffer *png.EncoderBuffer) {
	p.pool.Put(buffer)
}

var cardPNGEncoder = png.Encoder{CompressionLevel: png.BestSpeed, BufferPool: &pngBufferPool{}}

func encodePNG(f *os.File, im *image.RGBA) error {
	// The pooled encoder retains the image interface. Give it a separate
	// header so the pixel data can be released as soon as encoding finishes.
	raster := *im
	err := cardPNGEncoder.Encode(f, &raster)
	raster.Pix = nil
	return err
}

func encodeJPEGFile(path string, im image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return jpeg.Encode(f, im, &jpeg.Options{Quality: 85})
}
