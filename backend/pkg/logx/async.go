package logx

import (
	"io"
	"sync"
	"time"
)

// AsyncWriter wraps an io.Writer (such as os.Stdout in production servers) with a bounded
// channel buffer and background batch flush goroutine so high-throughput log lines never block
// request or worker goroutines on synchronous OS file write syscalls.
type AsyncWriter struct {
	dst    io.Writer
	ch     chan []byte
	stopCh chan struct{}
	doneCh chan struct{}
	once   sync.Once
}

// NewAsyncWriter starts a background writer flushing buffered chunks to dst.
func NewAsyncWriter(dst io.Writer, capacity int) *AsyncWriter {
	if capacity <= 0 {
		capacity = 8192
	}
	aw := &AsyncWriter{
		dst:    dst,
		ch:     make(chan []byte, capacity),
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
	go aw.loop()
	return aw
}

func (a *AsyncWriter) Write(p []byte) (int, error) {
	cp := make([]byte, len(p))
	copy(cp, p)
	select {
	case a.ch <- cp:
		return len(p), nil
	default:
		// Buffer full: drop cleanly rather than stalling the hot request path
		return len(p), nil
	}
}

func (a *AsyncWriter) loop() {
	defer close(a.doneCh)
	tk := time.NewTicker(10 * time.Millisecond)
	defer tk.Stop()

	var buf []byte
	flush := func() {
		if len(buf) > 0 {
			_, _ = a.dst.Write(buf)
			buf = buf[:0]
		}
	}

	for {
		select {
		case msg := <-a.ch:
			buf = append(buf, msg...)
			// Coalesce immediately available items up to 64KB before writing
		drain:
			for len(buf) < 64*1024 {
				select {
				case more := <-a.ch:
					buf = append(buf, more...)
				default:
					break drain
				}
			}
			flush()
		case <-tk.C:
			flush()
		case <-a.stopCh:
			for {
				select {
				case msg := <-a.ch:
					buf = append(buf, msg...)
					if len(buf) >= 64*1024 {
						flush()
					}
				default:
					flush()
					return
				}
			}
		}
	}
}

// Close flushes all remaining buffered log lines and stops the background writer.
func (a *AsyncWriter) Close() error {
	a.once.Do(func() {
		close(a.stopCh)
		<-a.doneCh
	})
	return nil
}
