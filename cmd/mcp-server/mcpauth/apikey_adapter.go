package mcpauth

import (
	"context"

	"github.com/gosom/google-maps-scraper/models"
	"github.com/gosom/google-maps-scraper/web/auth"
)

type APIKeyAdapter struct {
	ServerSecret []byte
	Repo         models.APIKeyRepository
}

func (a *APIKeyAdapter) Validate(ctx context.Context, raw string) (userID, keyID, tier string, err error) {
	uid, kid, err := auth.ValidateAPIKey(ctx, raw, a.ServerSecret, a.Repo)
	if err != nil {
		return "", "", "", err
	}
	// TODO(chunk-4): derive tier from billing/subscription instead of hard-coding.
	// The current APIKey model carries no tier field; OAuth tokens will likewise
	// need their tier resolved via the same lookup path.
	return uid, kid, "free", nil
}
