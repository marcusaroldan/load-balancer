package controlplane

import (
	"fmt"
	"net/http"
	"strings"
)

// prometheusContentType is returned when the client negotiates for it via
// Accept: text/plain (Prometheus scrapers); the default /metrics response is
// the JSON document the dashboard consumes.
const prometheusContentType = "text/plain; version=0.0.4; charset=utf-8"

// wantsPrometheus reports content negotiation for the exposition format.
func wantsPrometheus(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "text/plain")
}

// writePrometheus renders aggregate metrics. Cardinality is deliberately
// zero-label: only aggregates are exposed here so metrics can never become a
// side channel for per-backend routing state (PROD-009 boundary).
func (h *APIHandler) writePrometheus(w http.ResponseWriter, counters requestCounters, healthy, total, connections, pools int) {
	var b strings.Builder
	writeMetric(&b, "lb_requests_total", "counter", float64(counters.Total), "Requests proxied by the data plane")
	writeMetric(&b, "lb_requests_success_total", "counter", float64(counters.Success), "Requests completed successfully")
	writeMetric(&b, "lb_requests_gateway_failures_total", "counter", float64(counters.GatewayFailures), "Upstream failures answered as 502")
	writeMetric(&b, "lb_requests_route_failures_total", "counter", float64(counters.RouteFailures), "Selections rejected or unanswered answered as 503")
	writeGauge(&b, "lb_request_average_latency_ms", counters.AverageLatencyMS, "Mean request latency")
	writeGauge(&b, "lb_active_connections", float64(connections), "Connections currently reserved on backends")
	writeGauge(&b, "lb_backends_healthy", float64(healthy), "Backends currently eligible for selection")
	writeGauge(&b, "lb_backends_total", float64(total), "Configured backends")
	writeGauge(&b, "lb_pools", float64(pools), "Configured pools")
	w.Header().Set("Content-Type", prometheusContentType)
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, b.String())
}

func writeMetric(b *strings.Builder, name, kind string, value float64, help string) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s %s\n%s %s\n", name, help, name, kind, name, formatFloat(value))
}

func writeGauge(b *strings.Builder, name string, value float64, help string) {
	writeMetric(b, name, "gauge", value, help)
}

func formatFloat(v float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.3f", v), "0"), ".")
}
