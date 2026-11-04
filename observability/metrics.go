package observability

import (
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Metrics хранит ограниченный по cardinality набор HTTP-метрик.
type Metrics struct {
	mu        sync.RWMutex
	buckets   []float64
	requests  map[metricKey]uint64
	errors    map[metricKey]uint64
	durations []uint64
	count     uint64
	sum       float64
	active    int64
}

type metricKey struct {
	method string
	status int
}

// NewMetrics создаёт registry. Пустые buckets используют HTTP defaults.
func NewMetrics(buckets ...time.Duration) (*Metrics, error) {
	if len(buckets) == 0 {
		buckets = []time.Duration{5 * time.Millisecond, 10 * time.Millisecond, 25 * time.Millisecond, 50 * time.Millisecond, 100 * time.Millisecond, 250 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second, 5 * time.Second}
	}
	seconds := make([]float64, len(buckets))
	for index, bucket := range buckets {
		if bucket <= 0 || index > 0 && bucket <= buckets[index-1] {
			return nil, fmt.Errorf("observability: histogram buckets должны быть положительными и строго возрастать")
		}
		seconds[index] = bucket.Seconds()
	}
	return &Metrics{
		buckets: seconds, requests: make(map[metricKey]uint64), errors: make(map[metricKey]uint64), durations: make([]uint64, len(seconds)),
	}, nil
}

func (metrics *Metrics) begin() {
	if metrics == nil {
		return
	}
	metrics.mu.Lock()
	metrics.active++
	metrics.mu.Unlock()
}

func (metrics *Metrics) observe(method string, status int, duration time.Duration, failed bool) {
	if metrics == nil {
		return
	}
	method = metricMethod(method)
	if status < 100 || status > 599 {
		status = http.StatusInternalServerError
	}
	key := metricKey{method: method, status: status}
	seconds := duration.Seconds()
	metrics.mu.Lock()
	metrics.active--
	metrics.requests[key]++
	if failed || status >= http.StatusInternalServerError {
		metrics.errors[key]++
	}
	metrics.count++
	metrics.sum += seconds
	for index, upper := range metrics.buckets {
		if seconds <= upper {
			metrics.durations[index]++
		}
	}
	metrics.mu.Unlock()
}

func metricMethod(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions, http.MethodConnect, http.MethodTrace:
		return method
	default:
		return "OTHER"
	}
}

// ServeHTTP экспортирует registry в Prometheus text exposition format.
func (metrics *Metrics) ServeHTTP(writer http.ResponseWriter, _ *http.Request) {
	writer.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	if metrics == nil {
		http.Error(writer, "metrics registry is nil", http.StatusServiceUnavailable)
		return
	}
	metrics.mu.RLock()
	buckets := slices.Clone(metrics.buckets)
	durations := slices.Clone(metrics.durations)
	requests := cloneMetricMap(metrics.requests)
	errorsByKey := cloneMetricMap(metrics.errors)
	count, sum, active := metrics.count, metrics.sum, metrics.active
	metrics.mu.RUnlock()

	_, _ = io.WriteString(writer, "# HELP chitu_http_requests_total Total HTTP requests.\n# TYPE chitu_http_requests_total counter\n")
	writeMetricMap(writer, "chitu_http_requests_total", requests)
	_, _ = io.WriteString(writer, "# HELP chitu_http_errors_total HTTP responses with status 5xx.\n# TYPE chitu_http_errors_total counter\n")
	writeMetricMap(writer, "chitu_http_errors_total", errorsByKey)
	_, _ = fmt.Fprintf(writer, "# HELP chitu_http_requests_active Active HTTP requests.\n# TYPE chitu_http_requests_active gauge\nchitu_http_requests_active %d\n", active)
	_, _ = io.WriteString(writer, "# HELP chitu_http_request_duration_seconds HTTP request duration.\n# TYPE chitu_http_request_duration_seconds histogram\n")
	for index, upper := range buckets {
		_, _ = fmt.Fprintf(writer, "chitu_http_request_duration_seconds_bucket{le=%q} %d\n", strconv.FormatFloat(upper, 'g', -1, 64), durations[index])
	}
	_, _ = fmt.Fprintf(writer, "chitu_http_request_duration_seconds_bucket{le=\"+Inf\"} %d\nchitu_http_request_duration_seconds_sum %s\nchitu_http_request_duration_seconds_count %d\n", count, strconv.FormatFloat(sum, 'g', -1, 64), count)
}

func cloneMetricMap(source map[metricKey]uint64) map[metricKey]uint64 {
	result := make(map[metricKey]uint64, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func writeMetricMap(writer io.Writer, name string, values map[metricKey]uint64) {
	keys := make([]metricKey, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.SortFunc(keys, func(left, right metricKey) int {
		if compared := strings.Compare(left.method, right.method); compared != 0 {
			return compared
		}
		return left.status - right.status
	})
	for _, key := range keys {
		_, _ = fmt.Fprintf(writer, "%s{method=%q,status=%q} %d\n", name, key.method, strconv.Itoa(key.status), values[key])
	}
}
