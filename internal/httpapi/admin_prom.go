package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	promTimeout  = 5 * time.Second
	promMaxBody  = 1 << 20
	promCacheTTL = 15 * time.Second
)

// promClient asks the Prometheus HTTP API of admin.prometheusURL. It sends no credentials, follows no
// redirects (a redirect could lead to another host), caps the response and the time, and caches answers briefly,
// so the admin streams and polls do not multiply queries.
type promClient struct {
	base string
	hc   *http.Client

	mu    sync.Mutex
	cache map[string]promCached
	now   func() time.Time
}

type promCached struct {
	at   time.Time
	body []byte
}

func newPromClient(base string) *promClient {
	return &promClient{
		base:  strings.TrimRight(base, "/"),
		hc:    &http.Client{Timeout: promTimeout, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		cache: map[string]promCached{},
		now:   time.Now,
	}
}

type promSample struct {
	Labels map[string]string
	Value  float64
}

type promSeries struct {
	Labels map[string]string
	Points []promPoint
}

type promPoint struct {
	T time.Time
	V float64
}

type promResponse struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Data   struct {
		Result []struct {
			Metric map[string]string `json:"metric"`
			Value  []any             `json:"value"`
			Values [][]any           `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

func (c *promClient) get(ctx context.Context, path string, q url.Values) (*promResponse, error) {
	u := c.base + path + "?" + q.Encode()
	c.mu.Lock()
	hit, ok := c.cache[u]
	c.mu.Unlock()
	body := hit.body
	if !ok || c.now().Sub(hit.at) > promCacheTTL {
		var err error
		if body, err = c.fetch(ctx, u); err != nil {
			return nil, err
		}
		c.mu.Lock()
		if len(c.cache) > 256 {
			c.cache = map[string]promCached{}
		}
		c.cache[u] = promCached{at: c.now(), body: body}
		c.mu.Unlock()
	}
	var r promResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("prometheus: bad response: %w", err)
	}
	if r.Status != "success" {
		return nil, fmt.Errorf("prometheus: %s", r.Error)
	}
	return &r, nil
}

func (c *promClient) fetch(ctx context.Context, u string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, promTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, errors.New("prometheus: request failed") // the URL stays out of logs and errors
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("prometheus: status %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, promMaxBody+1))
	if err != nil {
		return nil, errors.New("prometheus: reading the response failed")
	}
	if len(b) > promMaxBody {
		return nil, errors.New("prometheus: response too large")
	}
	return b, nil
}

// Instant runs an instant query. Samples that are NaN or infinite (no data in the window) are left out.
func (c *promClient) Instant(ctx context.Context, query string) ([]promSample, error) {
	r, err := c.get(ctx, "/api/v1/query", url.Values{"query": {query}})
	if err != nil {
		return nil, err
	}
	var out []promSample
	for _, x := range r.Data.Result {
		if _, v, ok := promPair(x.Value); ok {
			out = append(out, promSample{Labels: x.Metric, Value: v})
		}
	}
	return out, nil
}

// Range runs a range query. The end is rounded down to a multiple of step, so repeated calls share a cache entry.
func (c *promClient) Range(ctx context.Context, query string, window, step time.Duration) ([]promSeries, error) {
	end := c.now().Truncate(step)
	r, err := c.get(ctx, "/api/v1/query_range", url.Values{
		"query": {query}, "start": {strconv.FormatInt(end.Add(-window).Unix(), 10)},
		"end": {strconv.FormatInt(end.Unix(), 10)}, "step": {strconv.Itoa(int(step.Seconds()))},
	})
	if err != nil {
		return nil, err
	}
	var out []promSeries
	for _, x := range r.Data.Result {
		s := promSeries{Labels: x.Metric}
		for _, p := range x.Values {
			if t, v, ok := promPair(p); ok {
				s.Points = append(s.Points, promPoint{T: time.Unix(int64(t), 0).UTC(), V: v})
			}
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return fmt.Sprint(out[i].Labels) < fmt.Sprint(out[j].Labels) })
	return out, nil
}

// promPair reads [timestamp, "value"].
func promPair(p []any) (float64, float64, bool) {
	if len(p) != 2 {
		return 0, 0, false
	}
	t, _ := p[0].(float64)
	str, _ := p[1].(string)
	v, err := strconv.ParseFloat(str, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, 0, false
	}
	return t, v, true
}
