package gitaccount

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// gitlab is a GitLab OAuth application. Access tokens expire and every refresh rotates the refresh token.
type gitlab struct{ *oauth }

func (g *gitlab) AuthorizeURL(redirectURI, state, challenge string) string {
	return g.authorize("/oauth/authorize", "read_repository write_repository read_user", redirectURI, state, challenge, url.Values{"response_type": {"code"}})
}

func (g *gitlab) Exchange(ctx context.Context, code, verifier, redirectURI string) (Token, error) {
	return g.exchange(ctx, "/oauth/token", code, verifier, redirectURI)
}

func (g *gitlab) Refresh(ctx context.Context, refreshToken string) (Token, error) {
	return g.form(ctx, "refresh", g.ep.Web+"/oauth/token", url.Values{"client_id": {g.cfg.ClientID}, "client_secret": {g.secret},
		"refresh_token": {refreshToken}, "grant_type": {"refresh_token"}})
}

func (g *gitlab) Account(ctx context.Context, accessToken string) (Account, error) {
	var u struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
		Name     string `json:"name"`
	}
	if err := g.getJSON(ctx, "account", g.ep.API+"/user", accessToken, &u); err != nil {
		return Account{}, err
	}
	if u.ID == 0 || u.Username == "" {
		return Account{}, fmt.Errorf("account: incomplete answer")
	}
	return Account{ID: u.ID, Login: u.Username, DisplayName: orElse(u.Name, u.Username), NoReplyEmail: fmt.Sprintf("%d-%s@users.noreply.%s", u.ID, u.Username, g.cfg.Host)}, nil
}

// Revoke revokes the token that is given (the refresh token goes with the access token on GitLab).
func (g *gitlab) Revoke(ctx context.Context, t Token) error {
	v := url.Values{"client_id": {g.cfg.ClientID}, "client_secret": {g.secret}, "token": {t.Access}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.ep.Web+"/oauth/revoke", strings.NewReader(v.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	status, _, err := g.do(req)
	if err != nil {
		return fmt.Errorf("revoke: %w", errUnwrap(err))
	}
	// GitLab answers 200 for unknown tokens, too.
	if status != http.StatusOK {
		return providerError("revoke", status, "")
	}
	return nil
}
