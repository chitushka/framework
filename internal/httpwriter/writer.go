// Package httpwriter preserves optional HTTP writer capabilities across wrappers.
package httpwriter

import (
	"bufio"
	"io"
	"net"
	"net/http"
)

// Writer intercepts flush and hijack before delegating through Unwrap.
type Writer interface {
	http.ResponseWriter
	Unwrap() http.ResponseWriter
	FlushError() error
	Hijack() (net.Conn, *bufio.ReadWriter, error)
}

// Preserve exposes Flusher/Hijacker only when the underlying chain supports them.
// ResponseController still traverses the intercepting writer, so session commits
// and buffered responses are not bypassed by Flush/Hijack.
func Preserve(writer Writer) http.ResponseWriter {
	flush, hijack := capabilities(writer.Unwrap())
	base := &baseWriter{Writer: writer, flush: flush, hijack: hijack}
	switch {
	case flush && hijack:
		return &bothWriter{base}
	case flush:
		return &flushWriter{base}
	case hijack:
		return &hijackWriter{base}
	default:
		return base
	}
}

type baseWriter struct {
	Writer        Writer
	flush, hijack bool
}

func (w *baseWriter) HTTPWriterCapabilities() (bool, bool) { return w.flush, w.hijack }
func (w *baseWriter) Header() http.Header                  { return w.Writer.Header() }
func (w *baseWriter) WriteHeader(status int)               { w.Writer.WriteHeader(status) }
func (w *baseWriter) Write(p []byte) (int, error)          { return w.Writer.Write(p) }
func (w *baseWriter) Unwrap() http.ResponseWriter          { return w.Writer }
func (w *baseWriter) FlushError() error                    { return w.Writer.FlushError() }
func (w *baseWriter) Push(target string, options *http.PushOptions) error {
	if pusher, ok := w.Writer.(http.Pusher); ok {
		return pusher.Push(target, options)
	}
	for next := w.Writer.Unwrap(); next != nil; {
		if pusher, ok := next.(http.Pusher); ok {
			return pusher.Push(target, options)
		}
		unwrap, ok := next.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			break
		}
		next = unwrap.Unwrap()
	}
	return http.ErrNotSupported
}
func (w *baseWriter) ReadFrom(reader io.Reader) (int64, error) {
	if from, ok := w.Writer.(io.ReaderFrom); ok {
		return from.ReadFrom(reader)
	}
	return io.Copy(writerOnly{w.Writer}, reader)
}

type writerOnly struct{ io.Writer }
type flushWriter struct{ *baseWriter }

func (w *flushWriter) Flush() { _ = w.FlushError() }

type hijackWriter struct{ *baseWriter }

func (w *hijackWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) { return w.Writer.Hijack() }

type bothWriter struct{ *baseWriter }

func (w *bothWriter) Flush()                                       { _ = w.FlushError() }
func (w *bothWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) { return w.Writer.Hijack() }

func capabilities(writer http.ResponseWriter) (bool, bool) {
	flush, hijack := false, false
	for writer != nil {
		if known, ok := writer.(interface{ HTTPWriterCapabilities() (bool, bool) }); ok {
			f, h := known.HTTPWriterCapabilities()
			return flush || f, hijack || h
		}
		_, f := writer.(http.Flusher)
		_, fe := writer.(interface{ FlushError() error })
		_, h := writer.(http.Hijacker)
		flush, hijack = flush || f || fe, hijack || h
		unwrap, ok := writer.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			break
		}
		writer = unwrap.Unwrap()
	}
	return flush, hijack
}
