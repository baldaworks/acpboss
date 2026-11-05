// Package protocol guards SDK JSON-RPC connections with finite frame and write limits.
package protocol

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

// Open constructs an SDK connection. Release starts reads after caller state is installed.
func Open(handler acp.MethodHandler, out io.Writer, in io.Reader, limit int, timeout time.Duration) (*acp.Connection, func()) {
	gate := make(chan struct{})
	var once sync.Once
	c := acp.NewConnection(handler, &frameWriter{writer: out, limit: limit, timeout: timeout}, &frameReader{reader: bufio.NewReaderSize(in, 4096), limit: limit, gate: gate})
	// SDK diagnostics can contain raw peer frames. Only application errors are exposed.
	c.SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
	return c, func() { once.Do(func() { close(gate) }) }
}

type frameReader struct {
	reader  *bufio.Reader
	limit   int
	gate    <-chan struct{}
	pending []byte
}

func (r *frameReader) Read(b []byte) (int, error) {
	<-r.gate
	if len(r.pending) == 0 {
		var line []byte
		for {
			part, err := r.reader.ReadSlice('\n')
			if len(line)+len(part) > r.limit {
				return 0, fmt.Errorf("ACP frame exceeds limit")
			}
			line = append(line, part...)
			if errors.Is(err, bufio.ErrBufferFull) {
				continue
			}
			if err != nil {
				return 0, err
			}
			break
		}
		if !json.Valid(line) {
			return 0, fmt.Errorf("invalid ACP frame")
		}
		r.pending = line
	}
	n := copy(b, r.pending)
	r.pending = r.pending[n:]
	return n, nil
}

type frameWriter struct {
	writer  io.Writer
	limit   int
	timeout time.Duration
}

func (w *frameWriter) Write(b []byte) (int, error) {
	if len(b) > w.limit {
		return 0, fmt.Errorf("ACP frame exceeds limit")
	}
	if d, ok := w.writer.(interface{ SetWriteDeadline(time.Time) error }); ok {
		if err := d.SetWriteDeadline(time.Now().Add(w.timeout)); err != nil && !errors.Is(err, os.ErrNoDeadline) {
			return 0, err
		}
	}
	return w.writer.Write(b)
}

// RequestError retains protocol codes while hiding untrusted error payloads.
func RequestError(err error) *acp.RequestError {
	var re *acp.RequestError
	if errors.As(err, &re) {
		return re
	}
	if errors.Is(err, context.Canceled) {
		return acp.NewRequestCancelled(nil)
	}
	return acp.NewInternalError(nil)
}
