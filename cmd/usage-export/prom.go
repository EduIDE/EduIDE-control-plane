package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// sample is one element of an instant vector.
type sample struct {
	Labels map[string]string
	Value  float64
}

// prom reads through GET /api/v1/query only and counts the series every query id returned,
// so the export can tell a missing metric from a quiet one.
type prom struct {
	base   string
	client *http.Client
	found  map[string]int
	order  []string
}

func newProm(base string) (*prom, error) {
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("--prometheus must be an http(s) URL, got %q", base)
	}
	return &prom{base: strings.TrimRight(base, "/"), client: &http.Client{Timeout: 2 * time.Minute}, found: map[string]int{}}, nil
}

// query evaluates expr at time at. The id names the allowlist entry in context.json.
func (p *prom) query(ctx context.Context, id, expr string, at time.Time) ([]sample, error) {
	q := url.Values{"query": {expr}, "time": {strconv.FormatInt(at.Unix(), 10)}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.base+"/api/v1/query?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query %s: %w", id, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Value  [2]any            `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("query %s: HTTP %d: %w", id, resp.StatusCode, err)
	}
	if body.Status != "success" {
		return nil, fmt.Errorf("query %s: %s", id, body.Error)
	}
	var out []sample
	for _, r := range body.Data.Result {
		s, _ := r.Value[1].(string)
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			continue
		}
		out = append(out, sample{r.Metric, v})
	}
	if _, ok := p.found[id]; !ok {
		p.order = append(p.order, id)
	}
	p.found[id] += len(out)
	return out, nil
}

// missing lists the query ids that never returned a series.
func (p *prom) missing() []string {
	out := []string{}
	for _, id := range p.order {
		if p.found[id] == 0 {
			out = append(out, id)
		}
	}
	return out
}
