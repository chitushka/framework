package performance

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync/atomic"
	"testing"
)

type responseSink struct {
	header http.Header
	body   []byte
	status int
}

func (s *responseSink) Header() http.Header         { return s.header }
func (s *responseSink) WriteHeader(status int)      { s.status = status }
func (s *responseSink) Write(p []byte) (int, error) { s.body = p; return len(p), nil }

func BenchmarkRouting(b *testing.B) {
	for _, routes := range []int{10, 100, 1000} {
		b.Run(fmt.Sprintf("Routes%d", routes), func(b *testing.B) {
			for _, variant := range []string{"NetHTTP", "Chitu"} {
				b.Run(variant, func(b *testing.B) {
					handler, err := Routing(variant, routes)
					if err != nil {
						b.Fatal(err)
					}
					type worker struct {
						sink     responseSink
						requests []*http.Request
					}
					workers := make([]worker, runtime.GOMAXPROCS(0))
					for i := range workers {
						workers[i].sink.header = make(http.Header)
						for n := range routes {
							request := httptest.NewRequest("GET", fmt.Sprintf("http://localhost/items%d/%d", n, n%256+1), nil)
							workers[i].requests = append(workers[i].requests, request.Clone(context.Background()))
						}
					}
					var next atomic.Int64
					handler.ServeHTTP(&workers[0].sink, workers[0].requests[0])
					var warmed Payload
					if err := json.Unmarshal(workers[0].sink.body, &warmed); err != nil || workers[0].sink.status != 200 || warmed != (Payload{ID: "1", Name: Name()}) {
						b.Fatal("routing warm-up/result check failed", err)
					}
					b.ReportAllocs()
					b.ResetTimer()
					b.RunParallel(func(pb *testing.PB) {
						state := &workers[next.Add(1)-1]
						index := 0
						for pb.Next() {
							clear(state.sink.header)
							state.sink.status, state.sink.body = 0, nil
							handler.ServeHTTP(&state.sink, state.requests[index])
							index = (index + 1) % routes
						}
						runtime.KeepAlive(state)
					})
				})
			}
		})
	}
}

func operationBenchmark(b *testing.B, operation func() error) {
	b.Helper()
	if err := operation(); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if err := operation(); err != nil {
				b.Error(err)
				return
			}
		}
	})
}

func BenchmarkDI(b *testing.B) {
	for _, name := range []string{"Transient", "Singleton"} {
		b.Run(name, func(b *testing.B) {
			manual, framework, err := DI(name == "Singleton")
			if err != nil {
				b.Fatal(err)
			}
			b.Run("Manual", func(b *testing.B) { operationBenchmark(b, ResolveOperation(manual)) })
			b.Run("Container", func(b *testing.B) { operationBenchmark(b, ResolveOperation(framework)) })
		})
	}
}

func BenchmarkRendering(b *testing.B) {
	manual, framework, err := Rendering()
	if err != nil {
		b.Fatal(err)
	}
	b.Run("Template", func(b *testing.B) { operationBenchmark(b, RenderOperation(manual)) })
	b.Run("Renderer", func(b *testing.B) { operationBenchmark(b, RenderOperation(framework)) })
}

func BenchmarkSQLBuild(b *testing.B) {
	for _, dialect := range []string{"postgresql", "mysql"} {
		b.Run(dialect, func(b *testing.B) {
			manual, framework, err := SQLBuild(dialect)
			if err != nil {
				b.Fatal(err)
			}
			b.Run("Static", func(b *testing.B) {
				operationBenchmark(b, func() error { value := manual(); runtime.KeepAlive(value); return nil })
			})
			b.Run("Builder", func(b *testing.B) {
				operationBenchmark(b, func() error { value, err := framework(); runtime.KeepAlive(value); return err })
			})
		})
	}
}
