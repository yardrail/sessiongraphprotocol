// Package main implements the sgpd authentication helpers.
package main

import (
	"context"
	"strings"

	"connectrpc.com/connect"
)

// newBearerInterceptor returns a unary+streaming interceptor that validates
// the Authorization: Bearer <token> header on every request.
func newBearerInterceptor(token string) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if !validBearer(req.Header().Get("Authorization"), token) {
				return nil, connect.NewError(connect.CodeUnauthenticated, errInvalidBearerToken)
			}

			return next(ctx, req)
		}
	}
}

func validBearer(header, token string) bool {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return false
	}

	return strings.TrimPrefix(header, prefix) == token
}
