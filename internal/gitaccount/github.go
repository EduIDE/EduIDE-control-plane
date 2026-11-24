package gitaccount

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// github is a GitHub OAuth App (also on GitHub Enterprise Server). Its tokens do not expire and have no refresh token.
type github struct{ *oauth }

func (g *github) AuthorizeURL(redirectURI, state, challenge string) string {
	return g.authorize("/login/oauth/authorize", "repo", redirectURI, state, challenge, nil)
}

func (g *github) Exchange(ctx context.Context, code, verifier, redirectURI string) (Token, error) {
	return g.exchange(ctx, "/login/oauth/access_token", code, verifier, redirectURI)
}

func (g *github) Refresh(context.Context, string) (Token, error) { return Token{}, ErrNoRefresh }

func (g *github) Account(ctx context.Context, accessToken string) (Account, error) {
	var u struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
		Name  string `json:"name"`
	}
	if err := g.getJSON(ctx, "account", g.ep.API+"/user", accessToken, &u); err != nil {
		return Account{}, err
	}
	if u.ID == 0 || u.Login == "" {
		return Account{}, fmt.Errorf("account: incomplete answer")
	}
	host := "users.noreply.github.com"
	if !strings.EqualFold(g.cfg.Host, "github.com") {
		host = "users.noreply." + g.cfg.Host
	}
	return Account{ID: u.ID, Login: u.Login, DisplayName: orElse(u.Name, u.Login), NoReplyEmail: fmt.Sprintf("%d+%s@%s", u.ID, u.Login, host)}, nil
}

// Revoke deletes the grant of the user for this app, which also removes the token.
func (g *github) Revoke(ctx context.Context, t Token) error {
	body, _ := json.Marshal(map[string]string{"access_token": t.Access})
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, g.ep.API+"/applications/"+g.cfg.ClientID+"/grant", strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.SetBasicAuth(g.cfg.ClientID, g.secret)
	req.Header.Set("Content-Type", "application/json")
	status, _, err := g.do(req)
	if err != nil {
		return fmt.Errorf("revoke: %w", errUnwrap(err))
	}
	if status != http.StatusNoContent && status != http.StatusNotFound {
		return providerError("revoke", status, "")
	}
	return nil
}

func orElse(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
