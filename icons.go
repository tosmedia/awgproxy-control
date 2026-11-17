package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
	"runtime"
)

// Значки рисуются в коде: цветной кружок. Mac — PNG, Windows — ICO с PNG внутри.
var (
	iconGreen  = makeIcon(color.NRGBA{0x2e, 0xb8, 0x5c, 0xff})
	iconYellow = makeIcon(color.NRGBA{0xf2, 0xb1, 0x34, 0xff})
	iconRed    = makeIcon(color.NRGBA{0xe0, 0x45, 0x3a, 0xff})
	iconGray   = makeIcon(color.NRGBA{0x8e, 0x8e, 0x93, 0xff})
)

func makeIcon(c color.NRGBA) []byte {
	const n = 32
	img := image.NewNRGBA(image.Rect(0, 0, n, n))
	cx, cy, r := float64(n)/2, float64(n)/2, float64(n)/2-3
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			d := math.Hypot(float64(x)+0.5-cx, float64(y)+0.5-cy)
			a := r + 0.5 - d // сглаживание края
			if a <= 0 {
				continue
			}
			if a > 1 {
				a = 1
			}
			// светлое кольцо внутри, чтобы кружок читался на тёмной и светлой панели
			cc := c
			if d < r-5 && d > r-7.5 {
				cc = color.NRGBA{uint8(min(255, int(c.R)+60)), uint8(min(255, int(c.G)+60)), uint8(min(255, int(c.B)+60)), 255}
			}
			cc.A = uint8(a * 255)
			img.SetNRGBA(x, y, cc)
		}
	}
	var pb bytes.Buffer
	_ = png.Encode(&pb, img)
	if runtime.GOOS != "windows" {
		return pb.Bytes()
	}
	var ib bytes.Buffer
	binary.Write(&ib, binary.LittleEndian, uint16(0))  // reserved
	binary.Write(&ib, binary.LittleEndian, uint16(1))  // type: icon
	binary.Write(&ib, binary.LittleEndian, uint16(1))  // count
	ib.Write([]byte{n, n, 0, 0})                       // width, height, colors, reserved
	binary.Write(&ib, binary.LittleEndian, uint16(1))  // planes
	binary.Write(&ib, binary.LittleEndian, uint16(32)) // bpp
	binary.Write(&ib, binary.LittleEndian, uint32(pb.Len()))
	binary.Write(&ib, binary.LittleEndian, uint32(22))
	ib.Write(pb.Bytes())
	return ib.Bytes()
}
