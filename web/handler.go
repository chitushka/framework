package web

import (
	"errors"
	"fmt"
	"net/http"
)

// Handler обрабатывает запрос в контексте Chitu.
type Handler func(*Context) error

// Middleware оборачивает обработчик сквозной логикой.
type Middleware func(Handler) Handler

// ErrorHandler преобразует ошибку обработчика в HTTP-ответ.
type ErrorHandler func(*Context, error)

// HTTPError содержит безопасное публичное сообщение и HTTP-статус.
type HTTPError struct {
	Status  int
	Message string
	Err     error
}

// NewHTTPError создаёт ошибку HTTP-слоя.
func NewHTTPError(status int, message string, err error) *HTTPError {
	return &HTTPError{Status: status, Message: message, Err: err}
}

func (e *HTTPError) Error() string {
	if e.Err == nil {
		return e.Message
	}
	return fmt.Sprintf("%s: %v", e.Message, e.Err)
}

// Unwrap возвращает исходную ошибку.
func (e *HTTPError) Unwrap() error { return e.Err }

func defaultErrorHandler(context *Context, err error) {
	status := http.StatusInternalServerError
	message := http.StatusText(status)
	var httpError *HTTPError
	if errors.As(err, &httpError) {
		status = httpError.Status
		message = httpError.Message
	}
	_ = context.JSON(status, map[string]any{
		"error": map[string]any{
			"status":  status,
			"message": message,
		},
	})
}

// Recover преобразует панику последующих обработчиков в обычную ошибку.
func Recover(next Handler) Handler {
	return func(context *Context) (err error) {
		defer func() {
			if recovered := recover(); recovered != nil {
				err = fmt.Errorf("web: паника обработчика: %v", recovered)
			}
		}()
		return next(context)
	}
}
