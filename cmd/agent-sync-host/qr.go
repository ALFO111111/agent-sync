package main

import (
	"fmt"
	"io"

	qrcode "github.com/skip2/go-qrcode"
)

// renderQR 用半高块在终端画出二维码；深色终端直接用即可。
func renderQR(content string, out io.Writer) error {
	code, err := qrcode.New(content, qrcode.Medium)
	if err != nil {
		return err
	}
	code.DisableBorder = false
	bitmap := code.Bitmap()
	for y := 0; y < len(bitmap); y += 2 {
		for x := 0; x < len(bitmap[y]); x++ {
			top := bitmap[y][x]
			bottom := y+1 < len(bitmap) && bitmap[y+1][x]
			switch {
			case top && bottom:
				fmt.Fprint(out, "█")
			case top:
				fmt.Fprint(out, "▀")
			case bottom:
				fmt.Fprint(out, "▄")
			default:
				fmt.Fprint(out, " ")
			}
		}
		fmt.Fprintln(out)
	}
	return nil
}
