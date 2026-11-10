package bootstrap

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/GlassJob/job-center/internal/model"
	"github.com/GlassJob/job-center/internal/pkg/auth"
	"github.com/GlassJob/job-center/internal/pkg/idgen"
	"github.com/GlassJob/job-center/internal/repository/mysql"
)

// EnsureAdminClient 在库中无 client 且提供了 bootstrap secret 时创建首个管理端 client。
func EnsureAdminClient(ctx context.Context, store *mysql.Store, pepper, secret, name string, log *slog.Logger) (clientID string, err error) {
	n, err := store.CountClients(ctx)
	if err != nil {
		return "", err
	}
	if n > 0 {
		return "", nil
	}
	if secret == "" {
		log.Warn("no admin clients; set JC_BOOTSTRAP_CLIENT_SECRET to create one")
		return "", nil
	}
	if name == "" {
		name = "bootstrap"
	}
	enc, err := auth.EncryptSecret(pepper, secret)
	if err != nil {
		return "", err
	}
	id := idgen.New()
	c := &model.Client{
		ID:         id,
		Name:       name,
		SecretHash: auth.HashSecret(pepper, secret),
		SecretEnc:  enc,
		HashAlgo:   auth.HashAlgo,
		SecretHint: auth.SecretHint(secret),
		Status:     model.ClientStatusActive,
		Scopes:     []string{"*"},
		CreatedBy:  "bootstrap",
	}
	if err := store.CreateClient(ctx, c); err != nil {
		return "", err
	}
	log.Info("bootstrap admin client created",
		"client_id", id,
		"name", name,
		"secret_hint", c.SecretHint,
	)
	fmt.Printf("\n=== BOOTSTRAP ADMIN CLIENT ===\nclient_id: %s\nsecret:    %s\nscopes:    *\nSave the secret now; it will not be shown again.\n==============================\n\n", id, secret)
	return id, nil
}
