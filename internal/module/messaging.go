package module

import (
	"context"

	"go-decentralized/internal/api"
)

// SendFunc delivers req, as the message called name ("<module>.<message>"),
// to the node reachable at to, and decodes its reply into resp. The node's
// network carries it, over mutual TLS, directly or through a relay.
type SendFunc func(ctx context.Context, to api.Peer, name string, req, resp any) error

// Handler handles one kind of message sent to the node. decode unmarshals
// the request into v.
type Handler func(ctx context.Context, decode func(v any) error) (any, error)

// Receiver is implemented by modules that handle messages. The node routes
// messages called "<module>.<name>" to the handler Messages returns for name.
type Receiver interface {
	Messages() map[string]Handler
}

// HandlerFor adapts a typed function to a Handler.
func HandlerFor[Req, Resp any](h func(context.Context, Req) (Resp, error)) Handler {
	return func(ctx context.Context, decode func(any) error) (any, error) {
		var req Req
		if err := decode(&req); err != nil {
			return nil, err
		}
		return h(ctx, req)
	}
}

type (
	senderKey   struct{}
	untracedKey struct{}
)

// Sender returns the ID of the node that sent the message being handled, as
// it proved over TLS.
func Sender(ctx context.Context) string {
	id, _ := ctx.Value(senderKey{}).(string)
	return id
}

// WithSender records who sent a message, for Sender. The node calls it.
func WithSender(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, senderKey{}, id)
}

// Untraced marks ctx so that messages sent with it aren't traced, e.g. the
// debugging tools' own traffic.
func Untraced(ctx context.Context) context.Context {
	return context.WithValue(ctx, untracedKey{}, true)
}

// IsUntraced reports whether ctx was marked by Untraced.
func IsUntraced(ctx context.Context) bool {
	return ctx.Value(untracedKey{}) != nil
}
