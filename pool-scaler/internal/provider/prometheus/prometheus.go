// Package prometheus reads claim rates, pod ready times, memory usage and the
// claim history for training from the operator metrics in Prometheus. Series
// are keyed by template, the label of eduide_claim_total (docs/contracts/metrics.md).
package prometheus

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/prometheus/client_golang/api"
	v1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"

	"github.com/EduIDE/EduIDE-control-plane/pool-scaler/internal/strategy"
)

// Provider queries one namespace's operator metrics.
type Provider struct {
	API       v1.API
	Namespace string
	Timeout   time.Duration
}

// New connects to a Prometheus HTTP API.
func New(url, namespace string, timeout time.Duration) (*Provider, error) {
	c, err := api.NewClient(api.Config{Address: url})
	if err != nil {
		return nil, err
	}
	return &Provider{API: v1.NewAPI(c), Namespace: namespace, Timeout: timeout}, nil
}

func (p *Provider) query(ctx context.Context, q string, at time.Time) (model.Vector, error) {
	ctx, cancel := context.WithTimeout(ctx, p.Timeout)
	defer cancel()
	v, _, _, err := p.API.Query(ctx, q, at)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", q, err)
	}
	vec, ok := v.(model.Vector)
	if !ok {
		return nil, fmt.Errorf("%s: expected a vector, got %s", q, v.Type())
	}
	return vec, nil
}

func byTemplate(vec model.Vector) map[string]float64 {
	out := map[string]float64{}
	for _, s := range vec {
		v := float64(s.Value)
		if math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		out[string(s.Metric["template"])] = v
	}
	return out
}

// demand selects the claims that are one session each: it took an idle instance (claimed) or found none
// (empty). The result "conflict" counts every lost attempt of a claim that is retried, so under a burst it
// would count the same session several times.
const demand = `eduide_claim_total{namespace=%q,result=~"claimed|empty"}`

// ClaimRates returns claims per second over window, per template.
func (p *Provider) ClaimRates(ctx context.Context, window time.Duration, at time.Time) (map[string]float64, error) {
	vec, err := p.query(ctx, fmt.Sprintf(`sum by (template) (rate(`+demand+`[%s]))`,
		p.Namespace, model.Duration(window)), at)
	return byTemplate(vec), err
}

// ReadySeconds returns the p95 cold start time per template: the time a pool
// pod needs from creation to Ready.
func (p *Provider) ReadySeconds(ctx context.Context, at time.Time) (map[string]float64, error) {
	vec, err := p.query(ctx, fmt.Sprintf(`histogram_quantile(0.95, sum by (template, le) (rate(eduide_session_startup_seconds_bucket{namespace=%q,strategy="cold",result="ready"}[6h])))`,
		p.Namespace), at)
	return byTemplate(vec), err
}

// Scalar runs a query that returns one number.
func (p *Provider) Scalar(ctx context.Context, q string, at time.Time) (float64, error) {
	vec, err := p.query(ctx, q, at)
	if err != nil {
		return 0, err
	}
	if len(vec) != 1 {
		return 0, fmt.Errorf("%s: expected one series, got %d", q, len(vec))
	}
	return float64(vec[0].Value), nil
}

// ClaimHistory returns the claims per SlotLength per template in
// [from, to], queried a week at a time to stay under the points limit.
func (p *Provider) ClaimHistory(ctx context.Context, from, to time.Time) (map[string][]strategy.Sample, error) {
	return p.History(ctx, from, to, strategy.SlotLength)
}

// History returns claims per step per template in [from, to].
func (p *Provider) History(ctx context.Context, from, to time.Time, step time.Duration) (map[string][]strategy.Sample, error) {
	q := fmt.Sprintf(`sum by (template) (increase(`+demand+`[%s]))`, p.Namespace, model.Duration(step))
	out := map[string][]strategy.Sample{}
	chunk := 10000 * step
	for start := from.Truncate(step); start.Before(to); start = start.Add(chunk) {
		end := start.Add(chunk - step)
		if end.After(to) {
			end = to
		}
		cctx, cancel := context.WithTimeout(ctx, p.Timeout)
		v, _, _, err := p.API.QueryRange(cctx, q, v1.Range{Start: start, End: end, Step: step})
		cancel()
		if err != nil {
			return nil, fmt.Errorf("claim history: %w", err)
		}
		m, ok := v.(model.Matrix)
		if !ok {
			return nil, fmt.Errorf("claim history: expected a matrix, got %s", v.Type())
		}
		for _, series := range m {
			app := string(series.Metric["template"])
			for _, pt := range series.Values {
				if f := float64(pt.Value); !math.IsNaN(f) {
					out[app] = append(out[app], strategy.Sample{T: pt.Timestamp.Time(), V: f})
				}
			}
		}
	}
	return out, nil
}
