package mcpauth

import "context"

type Method string

const (
	MethodAPIKey Method = "api_key"
	MethodOAuth  Method = "oauth"
)

type Identity struct {
	UserID   string
	Method   Method
	APIKeyID string
	ClientID string
	Tier     string
	TokenJTI string
}

type identityKey struct{}

func With(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, id)
}

func From(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityKey{}).(Identity)
	return id, ok
}
