package agent

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	maxFeedbackBytes = 16 << 10
	feedbackTimeout  = 10 * time.Second
)

// InstanceHeader names the instance toward the service, next to the instance token as bearer.
const InstanceHeader = "X-EduIDE-Instance"

var feedbackClient = &http.Client{
	Timeout: feedbackTimeout,
	// The service answers directly; a redirect would carry the instance token elsewhere.
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// feedback forwards the body unchanged to the service with the instance token. Neither the body nor the token is logged.
func (a *Agent) feedback(w http.ResponseWriter, r *http.Request) {
	if a.FeedbackURL == "" {
		http.NotFound(w, r)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxFeedbackBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		code := http.StatusBadRequest
		if errors.As(err, &tooLarge) {
			code = http.StatusRequestEntityTooLarge
		}
		http.Error(w, "invalid feedback", code)
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, a.FeedbackURL, strings.NewReader(string(body)))
	if err != nil {
		http.Error(w, "feedback not available", http.StatusBadGateway)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+string(a.token))
	req.Header.Set(InstanceHeader, strings.Trim(a.PathPrefix, "/"))
	resp, err := feedbackClient.Do(req) // #nosec G704 -- URL comes from the operator's AGENT_FEEDBACK_URL
	if err != nil {
		a.log.Info("feedback not delivered")
		http.Error(w, "feedback not delivered", http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if v := resp.Header.Get("Retry-After"); v != "" {
		w.Header().Set("Retry-After", v)
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 4<<10))
}
