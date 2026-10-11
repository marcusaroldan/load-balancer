// Package healthprobe provides the tiny "probe a URL and exit nonzero on
// failure" behavior used by container HEALTHCHECK directives. It lives in
// the binary instead of curl/busybox because the runtime images are
// scratch: the service is its own health probe dependency.
package healthprobe

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// DefaultTimeout bounds a probe attempt; compose gives healthchecks 3s.
const DefaultTimeout = 2 * time.Second

// Probe GETs url and returns nil only for a 200 response. Any other
// outcome (transport error, non-200 status, timeout) is an error so the
// container is marked unhealthy.
func Probe(ctx context.Context, url string) error {
	client := &http.Client{Timeout: DefaultTimeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build probe request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("probe %s: %w", url, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
		resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("probe %s: status %d", url, resp.StatusCode)
	}
	return nil
}
