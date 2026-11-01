// Package web предоставляет HTTP-слой Chitu Framework.
package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// Context объединяет HTTP-запрос, ответ и параметры найденного маршрута.
// Контекст создаётся отдельно для каждого запроса.
type Context struct {
	Writer  http.ResponseWriter
	Request *http.Request
	params  map[string]string
}

// Param возвращает параметр пути. Для отсутствующего параметра возвращается пустая строка.
func (c *Context) Param(name string) string {
	return c.params[name]
}

// JSON сериализует значение в JSON и записывает HTTP-статус.
func (c *Context) JSON(status int, value any) error {
	c.Writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	c.Writer.WriteHeader(status)
	return json.NewEncoder(c.Writer).Encode(value)
}

// NoContent записывает ответ без тела.
func (c *Context) NoContent(status int) error {
	c.Writer.WriteHeader(status)
	return nil
}

// Redirect отправляет HTTP-редирект.
func (c *Context) Redirect(status int, location string) error {
	http.Redirect(c.Writer, c.Request, location, status)
	return nil
}

// BindJSON декодирует единственный JSON-объект и запрещает неизвестные поля.
func (c *Context) BindJSON(target any) error {
	if target == nil {
		return errors.New("web: target для JSON не может быть nil")
	}
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return NewHTTPError(http.StatusBadRequest, "некорректный JSON", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return NewHTTPError(http.StatusBadRequest, "тело должно содержать один JSON-объект", err)
	}
	return nil
}
