package dns

import (
	"context"
	"net"
)

type Resolver interface {
	LookupTXT(ctx context.Context, name string) ([]string, error)
	LookupHost(ctx context.Context, name string) ([]string, error)
}

type NetResolver struct {
	resolver *net.Resolver
}

func NewNetResolver() *NetResolver {
	return &NetResolver{resolver: net.DefaultResolver}
}

func (r *NetResolver) LookupTXT(ctx context.Context, name string) ([]string, error) {
	return r.resolver.LookupTXT(ctx, name)
}

func (r *NetResolver) LookupHost(ctx context.Context, name string) ([]string, error) {
	return r.resolver.LookupHost(ctx, name)
}
