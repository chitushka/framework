package web_test

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/chitushka/framework/web"
)

type xmlMessage struct {
	XMLName xml.Name `xml:"message"`
	Text    string   `xml:",chardata"`
}

func TestContextStructuredAndHTMLResponses(t *testing.T) {
	tests := []struct {
		name        string
		write       func(*web.Context) error
		contentType string
		body        string
	}{
		{
			name:        "html",
			write:       func(context *web.Context) error { return context.HTML(http.StatusCreated, "<h1>Chitu</h1>") },
			contentType: "text/html; charset=utf-8",
			body:        "<h1>Chitu</h1>",
		},
		{
			name: "json",
			write: func(context *web.Context) error {
				return context.JSON(http.StatusCreated, map[string]string{"name": "Chitu"})
			},
			contentType: "application/json; charset=utf-8",
			body:        "{\"name\":\"Chitu\"}\n",
		},
		{
			name: "xml",
			write: func(context *web.Context) error {
				return context.XML(http.StatusCreated, xmlMessage{Text: "Chitu"})
			},
			contentType: "application/xml; charset=utf-8",
			body:        xml.Header + "<message>Chitu</message>\n",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			context := &web.Context{Writer: response, Request: httptest.NewRequest(http.MethodGet, "/", nil)}
			if err := test.write(context); err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusCreated || response.Header().Get("Content-Type") != test.contentType {
				t.Fatalf("unexpected response: %d %s", response.Code, response.Header())
			}
			if response.Body.String() != test.body || !context.Written() {
				t.Fatalf("unexpected body or state: %q, written=%v", response.Body.String(), context.Written())
			}
		})
	}
}

func TestSerializationErrorDoesNotCommitResponse(t *testing.T) {
	response := httptest.NewRecorder()
	context := &web.Context{Writer: response, Request: httptest.NewRequest(http.MethodGet, "/", nil)}
	if err := context.JSON(http.StatusOK, make(chan int)); err == nil {
		t.Fatal("expected JSON serialization error")
	}
	if context.Written() {
		t.Fatal("serialization error committed response")
	}
	if err := context.HTML(http.StatusInternalServerError, "fallback"); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusInternalServerError || response.Body.String() != "fallback" {
		t.Fatalf("fallback was not written: %d %q", response.Code, response.Body.String())
	}
}

func TestContextStreamAndStreamErrors(t *testing.T) {
	response := httptest.NewRecorder()
	context := &web.Context{Writer: response, Request: httptest.NewRequest(http.MethodGet, "/", nil)}
	if err := context.Stream(http.StatusOK, "text/plain", strings.NewReader("stream")); err != nil {
		t.Fatal(err)
	}
	if response.Body.String() != "stream" || response.Header().Get("Content-Type") != "text/plain" {
		t.Fatalf("unexpected stream response: %s %s", response.Body.String(), response.Header())
	}

	want := errors.New("read failure")
	response = httptest.NewRecorder()
	context = &web.Context{Writer: response, Request: httptest.NewRequest(http.MethodGet, "/", nil)}
	err := context.Stream(http.StatusOK, "", io.MultiReader(strings.NewReader("partial"), errorReader{err: want}))
	if !errors.Is(err, want) || !context.Written() {
		t.Fatalf("stream error was not preserved: %v", err)
	}
	if response.Header().Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("unexpected default Content-Type: %s", response.Header().Get("Content-Type"))
	}
	if err := (&web.Context{Writer: httptest.NewRecorder()}).Stream(http.StatusOK, "text/plain", nil); err == nil {
		t.Fatal("expected nil reader error")
	}
}

func TestContextFileAndDownload(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "report.txt")
	content := []byte("file body")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	context := &web.Context{Writer: response, Request: httptest.NewRequest(http.MethodGet, "/file", nil)}
	if err := context.File(http.StatusOK, path); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(response.Body.Bytes(), content) {
		t.Fatalf("unexpected file body: %q", response.Body.Bytes())
	}
	if !strings.HasPrefix(response.Header().Get("Content-Type"), "text/plain") ||
		response.Header().Get("Content-Length") != strconv.Itoa(len(content)) ||
		response.Header().Get("Last-Modified") == "" {
		t.Fatalf("missing file headers: %s", response.Header())
	}

	response = httptest.NewRecorder()
	context = &web.Context{Writer: response, Request: httptest.NewRequest(http.MethodGet, "/download", nil)}
	if err := context.Download(http.StatusOK, path, "отчёт.txt"); err != nil {
		t.Fatal(err)
	}
	disposition, parameters, err := mime.ParseMediaType(response.Header().Get("Content-Disposition"))
	if err != nil || disposition != "attachment" || parameters["filename"] != "отчёт.txt" {
		t.Fatalf("unexpected Content-Disposition: %q, %v", response.Header().Get("Content-Disposition"), err)
	}

	context = &web.Context{Writer: httptest.NewRecorder()}
	if err := context.File(http.StatusOK, filepath.Join(directory, "missing")); err == nil || context.Written() {
		t.Fatalf("missing file must fail before response is committed: %v", err)
	}
	if err := context.Download(http.StatusOK, path, ""); err == nil {
		t.Fatal("expected empty download name error")
	}
}

func TestResponseWriteErrorsAndDoubleWrite(t *testing.T) {
	want := errors.New("write failure")
	writer := &failingResponseWriter{err: want}
	context := &web.Context{Writer: writer, Request: httptest.NewRequest(http.MethodGet, "/", nil)}
	if err := context.HTML(http.StatusOK, "body"); !errors.Is(err, want) {
		t.Fatalf("write error was not preserved: %v", err)
	}
	if writer.writeHeaders != 1 || !context.Written() {
		t.Fatalf("unexpected write state: headers=%d written=%v", writer.writeHeaders, context.Written())
	}
	if err := context.NoContent(http.StatusNoContent); err == nil {
		t.Fatal("expected double write error")
	}
}

func TestDefaultErrorHandlerDoesNotWriteSecondResponse(t *testing.T) {
	want := errors.New("write failure")
	writer := &failingResponseWriter{err: want}
	router := web.NewRouter()
	if err := router.GET("/failure", func(context *web.Context) error {
		return context.HTML(http.StatusOK, "body")
	}); err != nil {
		t.Fatal(err)
	}
	router.ServeHTTP(writer, httptest.NewRequest(http.MethodGet, "/failure", nil))
	if writer.writeHeaders != 1 || writer.writes != 1 {
		t.Fatalf("error handler attempted a second response: headers=%d writes=%d", writer.writeHeaders, writer.writes)
	}

	response := httptest.NewRecorder()
	router = web.NewRouter()
	_ = router.GET("/serialization", func(context *web.Context) error {
		return context.JSON(http.StatusOK, make(chan int))
	})
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/serialization", nil))
	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), `"status":500`) {
		t.Fatalf("serialization error did not reach default handler: %d %s", response.Code, response.Body.String())
	}
}

func TestResponseValidationAndRedirect(t *testing.T) {
	context := &web.Context{Writer: httptest.NewRecorder(), Request: httptest.NewRequest(http.MethodGet, "/old", nil)}
	if err := context.Redirect(http.StatusFound, "/new"); err != nil {
		t.Fatal(err)
	}
	if context.Writer.Header().Get("Location") != "/new" || !context.Written() {
		t.Fatalf("unexpected redirect: %s", context.Writer.Header())
	}

	for name, err := range map[string]error{
		"invalid status":   (&web.Context{Writer: httptest.NewRecorder()}).NoContent(99),
		"invalid redirect": (&web.Context{Writer: httptest.NewRecorder()}).Redirect(http.StatusOK, "/new"),
		"nil writer":       (&web.Context{}).HTML(http.StatusOK, "body"),
	} {
		if err == nil {
			t.Fatalf("%s: expected validation error", name)
		}
	}
}

type errorReader struct {
	err error
}

func (reader errorReader) Read([]byte) (int, error) { return 0, reader.err }

type failingResponseWriter struct {
	header       http.Header
	err          error
	writeHeaders int
	writes       int
}

func (writer *failingResponseWriter) Header() http.Header {
	if writer.header == nil {
		writer.header = make(http.Header)
	}
	return writer.header
}

func (writer *failingResponseWriter) WriteHeader(int) { writer.writeHeaders++ }

func (writer *failingResponseWriter) Write([]byte) (int, error) {
	writer.writes++
	return 0, writer.err
}
