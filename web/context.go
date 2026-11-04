// Package web предоставляет HTTP-слой Chitu Framework.
package web

import (
	"bufio"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/chitushka/framework/internal/httpwriter"
)

// Context объединяет HTTP-запрос, ответ и параметры найденного маршрута.
// Контекст создаётся отдельно для каждого запроса.
type Context struct {
	Writer   http.ResponseWriter
	Request  *http.Request
	params   map[string]string
	route    string
	name     string
	written  bool
	response *responseWriteRecorder
}

// Param возвращает параметр пути. Для отсутствующего параметра возвращается пустая строка.
func (c *Context) Param(name string) string {
	return c.params[name]
}

// Params возвращает копию параметров текущего маршрута.
func (c *Context) Params() map[string]string {
	if c == nil {
		return nil
	}
	return maps.Clone(c.params)
}

// RoutePattern возвращает зарегистрированный шаблон текущего маршрута.
func (c *Context) RoutePattern() string {
	if c == nil {
		return ""
	}
	return c.route
}

// RouteName возвращает имя текущего маршрута или пустую строку.
func (c *Context) RouteName() string {
	if c == nil {
		return ""
	}
	return c.name
}

// Written reports final output or hijack at the Router's writer boundary, including
// direct writes. Buffered intent is not a committed response. Standalone contexts
// track writes through their response helpers only.
func (c *Context) Written() bool {
	if c.response != nil {
		return c.response.started
	}
	return c.written
}

// HTML записывает HTML-ответ в UTF-8.
func (c *Context) HTML(status int, value string) error {
	return c.write(status, "text/html; charset=utf-8", []byte(value))
}

// JSON сериализует значение в JSON и записывает HTTP-статус.
func (c *Context) JSON(status int, value any) error {
	if err := c.begin(); err != nil {
		return err
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return responseError("сериализация JSON", err)
	}
	payload = append(payload, '\n')
	return c.write(status, "application/json; charset=utf-8", payload)
}

// XML сериализует значение в XML с декларацией и записывает HTTP-статус.
func (c *Context) XML(status int, value any) error {
	if err := c.begin(); err != nil {
		return err
	}
	payload, err := xml.Marshal(value)
	if err != nil {
		return responseError("сериализация XML", err)
	}
	payload = append([]byte(xml.Header), payload...)
	payload = append(payload, '\n')
	return c.write(status, "application/xml; charset=utf-8", payload)
}

// NoContent записывает ответ без тела.
func (c *Context) NoContent(status int) error {
	return c.write(status, "", nil)
}

// Redirect отправляет HTTP-редирект.
func (c *Context) Redirect(status int, location string) error {
	if status < http.StatusMultipleChoices || status >= http.StatusBadRequest {
		return fmt.Errorf("web: статус редиректа должен быть в диапазоне 300-399: %d", status)
	}
	if c == nil || c.Request == nil {
		return errors.New("web: Request для redirect не задан")
	}
	if err := validateRedirect(c.Request, location); err != nil {
		return NewHTTPError(http.StatusBadRequest, "redirect destination is not allowed", err)
	}
	if err := c.begin(); err != nil {
		return err
	}
	recorder := &responseWriteRecorder{ResponseWriter: c.Writer}
	c.written = true
	http.Redirect(recorder, c.Request, location, status)
	if recorder.err != nil {
		return responseError("запись redirect", recorder.err)
	}
	return nil
}

// Stream копирует поток в HTTP-ответ. Пустой contentType означает application/octet-stream.
func (c *Context) Stream(status int, contentType string, reader io.Reader) error {
	if reader == nil {
		return errors.New("web: reader потока не может быть nil")
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if err := c.writeHeader(status, contentType); err != nil {
		return err
	}
	if status < 200 || status == 204 || status == 304 || (c.Request != nil && c.Request.Method == http.MethodHead) {
		return nil
	}
	if err := flushResponse(c.Writer); err != nil {
		return responseError("flush stream", err)
	}
	if _, err := io.Copy(flushingWriter{c.Writer, c.Request}, reader); err != nil {
		return responseError("запись stream", err)
	}
	return nil
}

// File sends a trusted application path. Never join client input onto a directory
// and pass it here: use FileFromRoot for filesystem containment instead.
func (c *Context) File(status int, path string) error {
	return c.sendFile(status, path, "")
}

// Download отправляет файл как attachment с безопасным именем загрузки.
func (c *Context) Download(status int, path, name string) error {
	if name == "" {
		return errors.New("web: имя загружаемого файла не может быть пустым")
	}
	return c.sendFile(status, path, name)
}

// FileFromRoot serves a relative slash-separated client-selected path using the
// OS traversal-resistant root. The caller owns root and must close it at shutdown.
func (c *Context) FileFromRoot(status int, root *os.Root, name string) error {
	return c.sendRootFile(status, root, name, "")
}

func (c *Context) DownloadFromRoot(status int, root *os.Root, name, downloadName string) error {
	if downloadName == "" {
		return errors.New("web: download name is required")
	}
	return c.sendRootFile(status, root, name, downloadName)
}

func (c *Context) sendRootFile(status int, root *os.Root, name, downloadName string) error {
	if err := c.begin(); err != nil {
		return err
	}
	if root == nil || !fs.ValidPath(name) || name == "." || strings.ContainsAny(name, "\\:\x00") {
		return errors.New("web: invalid rooted file path")
	}
	for _, char := range name {
		if char < 32 || char == 127 {
			return errors.New("web: invalid rooted file path")
		}
	}
	file, err := root.Open(name)
	if err != nil {
		return responseError("open rooted file", err)
	}
	return c.sendOpenedFile(status, file, downloadName)
}

// BindJSON decodes one JSON value, rejects unknown fields and requires a JSON media type.
func (c *Context) BindJSON(target any) error {
	if target == nil {
		return errors.New("web: target для JSON не может быть nil")
	}
	mediaType, _, err := mime.ParseMediaType(c.Request.Header.Get("Content-Type"))
	isJSON := mediaType == "application/json" || (strings.HasPrefix(mediaType, "application/") && strings.HasSuffix(mediaType, "+json") && len(mediaType) > len("application/+json"))
	if err != nil || !isJSON {
		return NewHTTPError(http.StatusUnsupportedMediaType, "Content-Type must be application/json or application/*+json", err)
	}
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return requestParseError(err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return NewHTTPError(http.StatusBadRequest, "body must contain one JSON value", nil)
		}
		return requestParseError(err)
	}
	return nil
}

func (c *Context) sendFile(status int, path, downloadName string) error {
	if err := c.begin(); err != nil {
		return err
	}
	file, err := os.Open(path)
	if err != nil {
		return responseError("открытие file", err)
	}
	return c.sendOpenedFile(status, file, downloadName)
}

func (c *Context) sendOpenedFile(status int, file *os.File, downloadName string) error {
	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil {
		return responseError("получение информации о file", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("web: file response requires a regular file")
	}

	contentType := mime.TypeByExtension(filepath.Ext(info.Name()))
	if contentType == "" {
		var sample [512]byte
		read, readErr := file.Read(sample[:])
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return responseError("определение Content-Type file", readErr)
		}
		contentType = http.DetectContentType(sample[:read])
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return responseError("перемотка file", err)
		}
	}

	if err := c.begin(); err != nil {
		return err
	}
	if downloadName != "" {
		disposition := mime.FormatMediaType("attachment", map[string]string{"filename": downloadName})
		c.Writer.Header().Set("Content-Disposition", disposition)
	}
	if status == http.StatusOK && c.Request != nil {
		c.Writer.Header().Set("Content-Type", contentType)
		recorder := &responseWriteRecorder{ResponseWriter: c.Writer, onWrite: func() { c.written = true }}
		request := c.Request
		// RFC 9110 defines Range only for GET. ServeContent also honors it on HEAD.
		if request.Method != http.MethodGet && request.Header.Get("Range") != "" {
			request = request.Clone(request.Context())
			request.Header.Del("Range")
		}
		http.ServeContent(httpwriter.Preserve(recorder), request, info.Name(), info.ModTime(), file)
		if recorder.err != nil {
			return responseError("file response", recorder.err)
		}
		return nil
	}
	c.Writer.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	c.Writer.Header().Set("Last-Modified", info.ModTime().UTC().Format(http.TimeFormat))
	return c.Stream(status, contentType, file)
}

func (c *Context) write(status int, contentType string, payload []byte) error {
	head := c != nil && c.Request != nil && c.Request.Method == http.MethodHead
	if head && status >= 200 && status != 204 && status != 304 && c.Writer != nil && c.Writer.Header().Get("Content-Length") == "" {
		c.Writer.Header().Set("Content-Length", strconv.Itoa(len(payload)))
	}
	if err := c.writeHeader(status, contentType); err != nil {
		return err
	}
	if len(payload) == 0 || head || status < 200 || status == 204 || status == 304 {
		return nil
	}
	written, err := c.Writer.Write(payload)
	if err != nil {
		return responseError("запись body", err)
	}
	if written != len(payload) {
		return responseError("запись body", io.ErrShortWrite)
	}
	return nil
}

func (c *Context) writeHeader(status int, contentType string) error {
	if status < 100 || status > 999 {
		return fmt.Errorf("web: некорректный HTTP-статус: %d", status)
	}
	if err := c.begin(); err != nil {
		return err
	}
	if contentType != "" {
		c.Writer.Header().Set("Content-Type", contentType)
	}
	if status >= 200 || status == 101 {
		c.written = true
	}
	c.Writer.WriteHeader(status)
	return nil
}

func (c *Context) begin() error {
	if c == nil || c.Writer == nil {
		return errors.New("web: ResponseWriter не задан")
	}
	if c.written {
		return errors.New("web: HTTP-ответ уже отправлен")
	}
	return nil
}

func responseError(operation string, err error) error {
	return fmt.Errorf("web: %s: %w", operation, err)
}

type responseWriteRecorder struct {
	http.ResponseWriter
	err     error
	onWrite func()
	started bool
}

func (writer *responseWriteRecorder) Unwrap() http.ResponseWriter { return writer.ResponseWriter }
func (writer *responseWriteRecorder) mark() {
	writer.started = true
	if writer.onWrite != nil {
		writer.onWrite()
	}
}
func (writer *responseWriteRecorder) WriteHeader(status int) {
	if status >= 200 || status == 101 {
		writer.mark()
	}
	writer.ResponseWriter.WriteHeader(status)
}
func (writer *responseWriteRecorder) FlushError() error {
	err := http.NewResponseController(writer.ResponseWriter).Flush()
	if err == nil {
		writer.mark()
	}
	return err
}
func (writer *responseWriteRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := http.NewResponseController(writer.ResponseWriter).Hijack()
	if err == nil {
		writer.mark()
	}
	return conn, rw, err
}

func (writer *responseWriteRecorder) Write(payload []byte) (int, error) {
	writer.mark()
	written, err := writer.ResponseWriter.Write(payload)
	if err != nil {
		writer.err = err
	} else if written != len(payload) {
		writer.err = io.ErrShortWrite
	}
	return written, err
}

func flushResponse(writer http.ResponseWriter) error {
	err := http.NewResponseController(writer).Flush()
	if errors.Is(err, http.ErrNotSupported) {
		return nil
	}
	return err
}

type flushingWriter struct {
	writer  http.ResponseWriter
	request *http.Request
}

func (writer flushingWriter) Write(payload []byte) (int, error) {
	if writer.request != nil {
		if err := writer.request.Context().Err(); err != nil {
			return 0, err
		}
	}
	n, err := writer.writer.Write(payload)
	if err != nil {
		return n, err
	}
	return n, flushResponse(writer.writer)
}
