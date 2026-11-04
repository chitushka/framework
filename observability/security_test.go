package observability

import (
	"fmt"
	"testing"
	"time"
)

func TestUntrustedHTTPMethodsCannotGrowMetricCardinality(t *testing.T) {
	metrics, err := NewMetrics()
	if err != nil {
		t.Fatal(err)
	}
	for i := range 10000 {
		metrics.begin()
		metrics.observe(fmt.Sprintf("ATTACK-%d", i), 404, time.Millisecond, false)
	}
	if len(metrics.requests) != 1 || metrics.requests[metricKey{method: "OTHER", status: 404}] != 10000 {
		t.Fatal("untrusted method labels grew metric registry")
	}
	metrics.begin()
	metrics.observe("GET", 999999, time.Millisecond, true)
	if len(metrics.errors) != 1 || metrics.errors[metricKey{method: "GET", status: 500}] != 1 {
		t.Fatal("invalid status label was not bounded")
	}
}
