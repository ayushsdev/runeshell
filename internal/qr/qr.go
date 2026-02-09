package qr

import (
	"io"

	"github.com/mdp/qrterminal/v3"
)

func RenderANSI(w io.Writer, data string) error {
	cfg := qrterminal.Config{
		Level:     qrterminal.M,
		Writer:    w,
		BlackChar: qrterminal.BLACK,
		WhiteChar: qrterminal.WHITE,
		QuietZone: 2,
	}
	qrterminal.GenerateWithConfig(data, cfg)
	return nil
}
