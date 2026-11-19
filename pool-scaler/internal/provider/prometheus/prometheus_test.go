package prometheus

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestQueries(t *testing.T) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		queries = append(queries, r.Form.Get("query"))
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/query_range") {
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[
				{"metric":{"template":"java-17"},"values":[[1763280000,"3"],[1763280900,"NaN"],[1763281800,"1.5"]]}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[
			{"metric":{"template":"java-17"},"value":[1763280000,"0.25"]},
			{"metric":{"template":"c"},"value":[1763280000,"NaN"]}]}}`))
	}))
	defer srv.Close()

	p, err := New(srv.URL, "eduide-test", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	rates, err := p.ClaimRates(ctx, 5*time.Minute, time.Now())
	if err != nil || rates["java-17"] != 0.25 {
		t.Fatalf("%v %v", rates, err)
	}
	if _, ok := rates["c"]; ok {
		t.Fatal("NaN must be dropped")
	}
	// Only claimed and empty are demand: a conflict is a lost attempt of a claim that is retried.
	if !strings.Contains(queries[0], `eduide_claim_total{namespace="eduide-test",result=~"claimed|empty"}[5m]`) {
		t.Fatalf("query %s", queries[0])
	}
	hist, err := p.ClaimHistory(ctx, time.Unix(1763280000, 0), time.Unix(1763281800, 0))
	if err != nil || len(hist["java-17"]) != 2 || hist["java-17"][1].V != 1.5 {
		t.Fatalf("%v %v", hist, err)
	}
	if !strings.Contains(queries[1], `increase(eduide_claim_total{namespace="eduide-test",result=~"claimed|empty"}`) {
		t.Fatalf("query %s", queries[1])
	}
	if _, err := p.ReadySeconds(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if q := queries[len(queries)-1]; !strings.Contains(q, `eduide_session_startup_seconds_bucket{namespace="eduide-test",strategy="cold",result="ready"}`) {
		t.Fatalf("query %s", q)
	}
	if _, err := p.Scalar(ctx, "x", time.Now()); err == nil {
		t.Fatal("two series are not a scalar")
	}
}
