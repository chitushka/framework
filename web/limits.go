package web

import (
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
)

// RequestLimits bounds request parsing. Zero fields select finite defaults;
// negative values are invalid. Limits count encoded body bytes, not decompressed data.
type RequestLimits struct {
	MaxBodyBytes       int64 // 8 MiB, including multipart framing
	MaxFormBytes       int64 // 1 MiB: URL-encoded body or multipart field names/values
	MaxMultipartMemory int64 // 1 MiB of multipart file data retained in memory
	MaxUploadBytes     int64 // 4 MiB per file
	MaxFormValues      int   // 1000 values, including query parameters on forms
	MaxFiles           int   // 16 files
}

func normalizeRequestLimits(limits RequestLimits) (RequestLimits, error) {
	fields := []struct {
		name     string
		value    *int64
		fallback int64
	}{
		{"MaxBodyBytes", &limits.MaxBodyBytes, 8 << 20},
		{"MaxFormBytes", &limits.MaxFormBytes, 1 << 20},
		{"MaxMultipartMemory", &limits.MaxMultipartMemory, 1 << 20},
		{"MaxUploadBytes", &limits.MaxUploadBytes, 4 << 20},
	}
	for _, field := range fields {
		if *field.value < 0 {
			return limits, fmt.Errorf("web: %s cannot be negative", field.name)
		}
		if *field.value == 0 {
			*field.value = field.fallback
		}
	}
	if limits.MaxFormValues < 0 || limits.MaxFiles < 0 {
		return limits, errors.New("web: form/file counts cannot be negative")
	}
	if limits.MaxFormValues == 0 {
		limits.MaxFormValues = 1000
	}
	if limits.MaxFiles == 0 {
		limits.MaxFiles = 16
	}
	return limits, nil
}

// NewLimitedHandler applies the same policy as Router to an arbitrary http.Handler.
// Install it outside middleware that reads bodies. Forms are parsed before next;
// temporary uploads are removed on return, including error and panic paths.
func NewLimitedHandler(next http.Handler, limits RequestLimits) (http.Handler, error) {
	if next == nil {
		return nil, errors.New("web: HTTP handler cannot be nil")
	}
	normalized, err := normalizeRequestLimits(limits)
	if err != nil {
		return nil, err
	}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		serveLimited(writer, request, normalized, defaultErrorHandler, next.ServeHTTP)
	}), nil
}

func serveLimited(writer http.ResponseWriter, request *http.Request, limits RequestLimits, onError ErrorHandler, next http.HandlerFunc) {
	// A clone keeps parser state and body wrappers local to this handler boundary.
	request = request.Clone(request.Context())
	defer func() {
		if request.MultipartForm != nil {
			_ = request.MultipartForm.RemoveAll()
		}
	}()
	if err := prepareRequest(writer, request, limits); err != nil {
		onError(&Context{Writer: writer, Request: request}, err)
		return
	}
	next(writer, request)
}

func prepareRequest(writer http.ResponseWriter, request *http.Request, limits RequestLimits) error {
	mediaType := ""
	if header := request.Header.Get("Content-Type"); header != "" {
		var err error
		mediaType, _, err = mime.ParseMediaType(header)
		if err != nil {
			return NewHTTPError(http.StatusUnsupportedMediaType, "invalid Content-Type", err)
		}
	}
	bodyLimit := limits.MaxBodyBytes
	if mediaType == "application/x-www-form-urlencoded" && limits.MaxFormBytes < bodyLimit {
		bodyLimit = limits.MaxFormBytes
	}
	if request.ContentLength > bodyLimit {
		return requestParseError(&http.MaxBytesError{Limit: bodyLimit})
	}
	if request.Body != nil {
		request.Body = http.MaxBytesReader(writer, request.Body, bodyLimit)
	}
	var err error
	switch mediaType {
	case "application/x-www-form-urlencoded":
		err = request.ParseForm()
	case "multipart/form-data":
		err = request.ParseMultipartForm(min(limits.MaxMultipartMemory, limits.MaxBodyBytes))
	default:
		return nil // JSON and raw streams are decoded by the selected handler.
	}
	if err != nil {
		return requestParseError(err)
	}
	var size int64
	count := 0
	for name, values := range request.Form {
		for _, value := range values {
			count++
			size += int64(len(name)) + int64(len(value))
			if count > limits.MaxFormValues || size > limits.MaxFormBytes {
				return NewHTTPError(http.StatusRequestEntityTooLarge, "form exceeds limits", nil)
			}
		}
	}
	files := 0
	if request.MultipartForm != nil {
		for _, uploads := range request.MultipartForm.File {
			for _, upload := range uploads {
				files++
				if files > limits.MaxFiles || upload.Size > limits.MaxUploadBytes {
					return NewHTTPError(http.StatusRequestEntityTooLarge, "upload exceeds limits", nil)
				}
			}
		}
	}
	return nil
}

func requestParseError(err error) error {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) || errors.Is(err, multipart.ErrMessageTooLarge) {
		return NewHTTPError(http.StatusRequestEntityTooLarge, "request body exceeds limits", err)
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return NewHTTPError(http.StatusRequestTimeout, "request body timeout", err)
	}
	return NewHTTPError(http.StatusBadRequest, "invalid request body", err)
}
