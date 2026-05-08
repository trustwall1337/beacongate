package google

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/trustwall1337/beacongate/engine/transport"
)

// QuickDiagnose performs an HTTP HEAD against url and returns whether the
// remote answered successfully. It is a thin helper used by tools and tests
// that want to test reachability without a full Roundtrip.
func QuickDiagnose(ctx context.Context, url string, timeout time.Duration) (transport.Diagnostics, error) {
	if url == "" {
		return transport.Diagnostics{}, errors.New("google transport: url required")
	}
	if timeout == 0 {
		timeout = defaultTimeout
	}
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return transport.Diagnostics{}, err
	}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return transport.Diagnostics{Healthy: false, Detail: err.Error()}, nil
	}
	defer resp.Body.Close()
	return transport.Diagnostics{
		Healthy: resp.StatusCode < 400,
		Latency: time.Since(start),
		Detail:  resp.Status,
	}, nil
}
