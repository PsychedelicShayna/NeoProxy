package api

import (
	"context"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopool"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func (s *Server) initNeoSources() {
	if s.neoPool == nil || s.neoRoute == nil || s.neoErr != nil {
		return
	}
	s.neoPool.ConfigureSources(func(provider string) coreauth.ProviderExecutor {
		s.cfgMu.RLock()
		cfg := s.cfg
		s.cfgMu.RUnlock()
		return executor.NewNeoPiExecutor(provider, cfg, func(ctx context.Context, auth *coreauth.Auth, model string) (*coreauth.Auth, error) {
			principal, ok := neo.PrincipalFromContext(ctx)
			if !ok {
				return nil, neo.ErrForbidden
			}
			// Manager retries remain inside one route reservation. Refresh the key
			// and user authority here too, so revocation cannot ride that reservation.
			var current neo.Principal
			var err error
			if principal.KeyID != "" {
				current, err = s.neoStore.RefreshPrincipal(ctx, principal)
			} else {
				current, err = s.neoStore.PrincipalForUser(ctx, principal.UserID)
				current.Session = principal.Session
				current.AccountIDs = principal.AccountIDs
			}
			if err != nil {
				return nil, err
			}
			return s.neoPool.PrepareSourceAuth(neo.WithPrincipal(ctx, current), auth, model)
		})
	}, s.publishNeoSourceModels)
}

// Shared catalogues are ordinary stored virtual models, with grants and atomic
// local-meter reservations. Never expose a second, unmetered selector path.
func (s *Server) publishNeoSourceModels(ctx context.Context, source neopool.Source) error {
	p, err := s.neoStore.PrincipalForUser(ctx, source.AdminID)
	if err != nil || p.Role != neo.RoleAdmin {
		return fmt.Errorf("source administrator is no longer active")
	}
	p.Session = true
	ctx = neo.WithPrincipal(ctx, p)
	return s.neoRoute.EnsureSourceModels(ctx, source)
}
