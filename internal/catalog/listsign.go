package catalog

import (
	"context"
	"net/http"
)

type listSignKey struct{}

// WithListSigner is ctx with sign called on each request for a model list
// made under it, last, once its headers are set: a list asked with a
// signature in place of a key (Amazon Bedrock with an AWS access key,
// #7687). An error from sign is the list's.
func WithListSigner(ctx context.Context, sign func(*http.Request) error) context.Context {
	return context.WithValue(ctx, listSignKey{}, sign)
}

func listSignerOf(ctx context.Context) func(*http.Request) error {
	sign, _ := ctx.Value(listSignKey{}).(func(*http.Request) error)
	return sign
}
