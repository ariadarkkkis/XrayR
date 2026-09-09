package limiter

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
	"golang.org/x/time/rate"
)

type Writer struct {
	writer  buf.Writer
	limiter *rate.Limiter
	w       io.Writer
}

type Reader struct {
	reader  buf.TimeoutReader
	limiter *rate.Limiter
}

func (l *Limiter) RateReader(reader buf.TimeoutReader, limiter *rate.Limiter) buf.TimeoutReader {
	return &Reader{reader: reader, limiter: limiter}
}

func (r *Reader) ReadMultiBuffer() (buf.MultiBuffer, error) {
	mb, err := r.reader.ReadMultiBuffer()
	if !mb.IsEmpty() {
		if waitErr := waitForTokens(r.limiter, int(mb.Len())); waitErr != nil && err == nil {
			err = waitErr
		}
	}
	return mb, err
}

func (r *Reader) ReadMultiBufferTimeout(timeout time.Duration) (buf.MultiBuffer, error) {
	mb, err := r.reader.ReadMultiBufferTimeout(timeout)
	if !mb.IsEmpty() {
		if waitErr := waitForTokens(r.limiter, int(mb.Len())); waitErr != nil && err == nil {
			err = waitErr
		}
	}
	return mb, err
}

func (r *Reader) Interrupt() {
	if wrapper, ok := r.reader.(*buf.TimeoutWrapperReader); ok {
		_ = common.Interrupt(wrapper.Reader)
		return
	}
	_ = common.Interrupt(r.reader)
}

func (l *Limiter) RateWriter(writer buf.Writer, limiter *rate.Limiter) buf.Writer {
	return &Writer{
		writer:  writer,
		limiter: limiter,
	}
}

func (w *Writer) Close() error {
	return common.Close(w.writer)
}

func (w *Writer) WriteMultiBuffer(mb buf.MultiBuffer) error {
	if err := waitForTokens(w.limiter, int(mb.Len())); err != nil {
		return err
	}
	return w.writer.WriteMultiBuffer(mb)
}

func waitForTokens(limiter *rate.Limiter, count int) error {
	if limiter.Burst() <= 0 {
		return fmt.Errorf("rate limiter burst must be positive")
	}
	for count > 0 {
		chunk := min(count, limiter.Burst())
		if err := limiter.WaitN(context.Background(), chunk); err != nil {
			return err
		}
		count -= chunk
	}
	return nil
}
