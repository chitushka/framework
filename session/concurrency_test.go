package session_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/chitushka/framework/session"
	"github.com/chitushka/framework/web"
)

// Two HTTP application instances deliberately load the same snapshot before
// either responds. The late response must not resurrect or overwrite it.
func TestConcurrentHTTPInstances(t *testing.T) {
	for _, transition := range []string{"update", "regenerate", "logout"} {
		t.Run(transition, func(t *testing.T) {
			backend := session.NewMemoryBackend()
			entered := make(chan int, 2)
			releases := []chan struct{}{make(chan struct{}), make(chan struct{})}
			servers := make([]*httptest.Server, 2)
			for i := range servers {
				manager, err := session.New(backend, session.Config{})
				if err != nil {
					t.Fatal(err)
				}
				router := web.NewRouter()
				if err := router.Use(manager.Middleware); err != nil {
					t.Fatal(err)
				}
				if err := router.GET("/seed", func(ctx *web.Context) error {
					return session.Must(ctx.Request.Context()).Set("role", "member")
				}); err != nil {
					t.Fatal(err)
				}
				if err := router.POST("/hold", func(ctx *web.Context) error {
					current := session.Must(ctx.Request.Context())
					if err := current.Set("role", "changed"); err != nil {
						return err
					}
					if i == 0 {
						switch transition {
						case "regenerate":
							if err := current.Regenerate(); err != nil {
								return err
							}
						case "logout":
							current.Destroy()
						}
					}
					entered <- i
					select {
					case <-releases[i]:
						return ctx.NoContent(204)
					case <-ctx.Request.Context().Done():
						return ctx.Request.Context().Err()
					}
				}); err != nil {
					t.Fatal(err)
				}
				servers[i] = httptest.NewServer(router)
				t.Cleanup(servers[i].Close)
			}
			// Release blocked handlers on every failure before closing test servers.
			t.Cleanup(func() {
				for _, release := range releases {
					select {
					case <-release:
					default:
						close(release)
					}
				}
			})
			client := &http.Client{Timeout: 3 * time.Second}
			response, err := client.Get(servers[0].URL + "/seed")
			if err != nil {
				t.Fatal(err)
			}
			cookie := response.Cookies()[0]
			if err := response.Body.Close(); err != nil {
				t.Fatal(err)
			}
			type result struct {
				response *http.Response
				err      error
			}
			results := []chan result{make(chan result, 1), make(chan result, 1)}
			for i := range servers {
				go func() {
					request, err := http.NewRequestWithContext(t.Context(), "POST", servers[i].URL+"/hold", nil)
					if err != nil {
						results[i] <- result{err: err}
						return
					}
					request.AddCookie(cookie)
					response, err := client.Do(request)
					results[i] <- result{response, err}
				}()
			}
			for range servers {
				select {
				case <-entered:
				case <-time.After(3 * time.Second):
					t.Fatal("handlers did not load snapshots")
				}
			}
			for i, want := range []int{204, 409} {
				close(releases[i])
				got := <-results[i]
				if got.err != nil {
					t.Fatal(got.err)
				}
				body, err := io.ReadAll(got.response.Body)
				if err != nil {
					t.Fatal(err)
				}
				if err := got.response.Body.Close(); err != nil {
					t.Fatal(err)
				}
				if got.response.StatusCode != want {
					t.Fatalf("instance %d: %d %s", i, got.response.StatusCode, body)
				}
				if i == 1 && len(got.response.Cookies()) > 0 {
					t.Fatal("stale response overwrote browser cookie")
				}
			}
		})
	}
}
