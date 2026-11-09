package server

import (
	"bytes"
	"io"

	"example.invalid/hearthlog/internal/model"
)

type boundedWriter struct {
	buf      bytes.Buffer
	overflow bool
}

func (b *boundedWriter) Write(p []byte) (int, error) {
	if b.overflow {
		return 0, io.ErrShortWrite
	}
	if b.buf.Len()+len(p) > model.MaxResultBytes {
		b.overflow = true
		return 0, io.ErrShortWrite
	}
	return b.buf.Write(p)
}

func (b *boundedWriter) Bytes() []byte {
	return b.buf.Bytes()
}
