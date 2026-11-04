package httpwriter

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type intercept struct{ http.ResponseWriter }

func (w *intercept) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *intercept) FlushError() error           { return http.NewResponseController(w.ResponseWriter).Flush() }
func (w *intercept) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return http.NewResponseController(w.ResponseWriter).Hijack()
}

type plain struct{ http.ResponseWriter }
type flushable struct{ http.ResponseWriter }

var errFlush = errors.New("flush failure")

func (*flushable) FlushError() error { return errFlush }

type hijackable struct{ http.ResponseWriter }

var errHijack = errors.New("hijack called")

func (*hijackable) Hijack() (net.Conn, *bufio.ReadWriter, error) { return nil, nil, errHijack }

type both struct{ *flushable }

func (*both) Hijack() (net.Conn, *bufio.ReadWriter, error) { return nil, nil, errHijack }

func TestPreserveOptionalInterfacesAcrossMultipleWrappers(t *testing.T) {
	for _, tc := range []struct {
		name          string
		next          func(http.ResponseWriter) http.ResponseWriter
		flush, hijack bool
	}{
		{"neither", func(w http.ResponseWriter) http.ResponseWriter { return &plain{w} }, false, false},
		{"flush", func(w http.ResponseWriter) http.ResponseWriter { return &flushable{w} }, true, false},
		{"hijack", func(w http.ResponseWriter) http.ResponseWriter { return &hijackable{w} }, false, true},
		{"both", func(w http.ResponseWriter) http.ResponseWriter { return &both{&flushable{w}} }, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			writer := tc.next(recorder)
			for range 3 {
				writer = Preserve(&intercept{writer})
			}
			_, flush := writer.(http.Flusher)
			_, hijack := writer.(http.Hijacker)
			if flush != tc.flush || hijack != tc.hijack {
				t.Fatalf("advertised flush=%v hijack=%v", flush, hijack)
			}
			err := http.NewResponseController(writer).Flush()
			var want error = http.ErrNotSupported
			if tc.flush {
				want = errFlush
			}
			if !errors.Is(err, want) {
				t.Fatalf("flush error=%v", err)
			}
			_, _, err = http.NewResponseController(writer).Hijack()
			want = http.ErrNotSupported
			if tc.hijack {
				want = errHijack
			}
			if !errors.Is(err, want) {
				t.Fatalf("hijack error=%v", err)
			}
			n, err := io.Copy(writer, io.LimitReader(strings.NewReader("body"), 4))
			if err != nil || n != 4 || recorder.Body.String() != "body" {
				t.Fatalf("ReadFrom=%d %v %s", n, err, recorder.Body.String())
			}
			if err := writer.(http.Pusher).Push("/asset", nil); !errors.Is(err, http.ErrNotSupported) {
				t.Fatalf("push=%v", err)
			}
		})
	}
}

type controlled struct {
	http.ResponseWriter
	deadline       time.Time
	duplex, pushed bool
}

func (w *controlled) SetWriteDeadline(deadline time.Time) error { w.deadline = deadline; return nil }
func (w *controlled) EnableFullDuplex() error                   { w.duplex = true; return nil }
func (w *controlled) Push(string, *http.PushOptions) error      { w.pushed = true; return nil }
func TestControllerTraversesUnwrapWithoutBypassingWrites(t *testing.T) {
	source := &controlled{ResponseWriter: httptest.NewRecorder()}
	writer := Preserve(&intercept{Preserve(&intercept{source})})
	deadline := time.Now().Add(time.Second)
	controller := http.NewResponseController(writer)
	if err := controller.SetWriteDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	if err := controller.EnableFullDuplex(); err != nil {
		t.Fatal(err)
	}
	if err := writer.(http.Pusher).Push("/asset", nil); err != nil {
		t.Fatal(err)
	}
	if source.deadline != deadline || !source.duplex || !source.pushed {
		t.Fatal("controller/push capabilities lost")
	}
}
