// Package web предоставляет HTTP-слой Chitu Framework.
package web

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"maps"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Context объединяет HTTP-запрос, ответ и параметры найденного маршрута.
// Контекст создаётся отдельно для каждого запроса.
type Context struct {
	Writer  http.ResponseWriter
	Request *http.Request
	params  map[string]string
	route   string
	name    string
	written bool
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

// Written сообщает, была ли уже начата запись ответа через Context.
func (c *Context) Written() bool {
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
	if _, err := io.Copy(c.Writer, reader); err != nil {
		return responseError("запись stream", err)
	}
	return nil
}

// File отправляет обычный файл с автоматически определённым Content-Type.
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
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return responseError("получение информации о file", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("web: file response требует обычный файл: %s", path)
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
	c.Writer.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	c.Writer.Header().Set("Last-Modified", info.ModTime().UTC().Format(http.TimeFormat))
	if downloadName != "" {
		disposition := mime.FormatMediaType("attachment", map[string]string{"filename": downloadName})
		c.Writer.Header().Set("Content-Disposition", disposition)
	}
	return c.Stream(status, contentType, file)
}

func (c *Context) write(status int, contentType string, payload []byte) error {
	if err := c.writeHeader(status, contentType); err != nil {
		return err
	}
	if len(payload) == 0 {
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
	c.written = true
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
	err error
}

func (writer *responseWriteRecorder) Write(payload []byte) (int, error) {
	written, err := writer.ResponseWriter.Write(payload)
	if err != nil {
		writer.err = err
	} else if written != len(payload) {
		writer.err = io.ErrShortWrite
	}
	return written, err
}
