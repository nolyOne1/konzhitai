package auth_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"yunling.local/platform/internal/auth"
)

func TestPostgresLoginRejectsCredentialSnapshotAfterConcurrentMutation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(context.Context, *pgxpool.Pool, *auth.PostgresRepository, string, string) error
	}{
		{
			name: "成员停用",
			mutate: func(ctx context.Context, _ *pgxpool.Pool, repository *auth.PostgresRepository, actorID, targetID string) error {
				_, err := repository.SetMemberEnabled(ctx, actorID, targetID, false)
				return err
			},
		},
		{
			name: "成员移除",
			mutate: func(ctx context.Context, _ *pgxpool.Pool, repository *auth.PostgresRepository, actorID, targetID string) error {
				_, err := repository.RemoveMember(ctx, actorID, targetID)
				return err
			},
		},
		{
			name: "管理员重置密码",
			mutate: func(ctx context.Context, _ *pgxpool.Pool, repository *auth.PostgresRepository, actorID, targetID string) error {
				hash, err := auth.HashPassword("reset-password-2026")
				if err != nil {
					return err
				}
				_, err = repository.ResetMemberPassword(ctx, actorID, targetID, hash)
				return err
			},
		},
		{
			name: "成员自行改密",
			mutate: func(ctx context.Context, db *pgxpool.Pool, repository *auth.PostgresRepository, _, targetID string) error {
				user, err := repository.FindByEmail(ctx, "viewer@example.com")
				if err != nil {
					return err
				}
				newHash, err := auth.HashPassword("changed-password-2026")
				if err != nil {
					return err
				}
				currentSessionHash := sha256.Sum256([]byte("target-session"))
				return auth.NewPostgresPasswordChangeStore(db).CommitPasswordChange(ctx, auth.PasswordChangeCommit{
					UserID: targetID, ExpectedHash: user.PasswordHash, NewHash: newHash,
					CurrentSessionHash: currentSessionHash[:], IPAddress: "203.0.113.8", ChangedAt: time.Now().UTC(),
				})
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, actorID, targetID := memberDatabase(t)
			repository := auth.NewPostgresRepository(db)
			users := &gatedUserRepository{
				delegate: repository,
				reached:  make(chan struct{}),
				release:  make(chan struct{}),
			}
			service := auth.NewService(users, repository)
			type loginResult struct {
				session auth.Session
				err     error
			}
			result := make(chan loginResult, 1)
			go func() {
				session, err := service.Login(context.Background(), "viewer@example.com", "member-password-2026")
				result <- loginResult{session: session, err: err}
			}()

			<-users.reached
			if err := test.mutate(context.Background(), db, repository, actorID, targetID); err != nil {
				t.Fatalf("执行并发变更：%v", err)
			}
			var sessionsBeforeRelease int
			if err := db.QueryRow(context.Background(), `SELECT count(*) FROM sessions WHERE user_id=$1 AND revoked_at IS NULL`, targetID).Scan(&sessionsBeforeRelease); err != nil {
				t.Fatal(err)
			}
			close(users.release)
			outcome := <-result
			if !errors.Is(outcome.err, auth.ErrInvalidCredentials) {
				t.Fatalf("并发变更后的旧凭据不得创建会话：session=%+v err=%v", outcome.session, outcome.err)
			}
			var sessionsAfterLogin int
			if err := db.QueryRow(context.Background(), `SELECT count(*) FROM sessions WHERE user_id=$1 AND revoked_at IS NULL`, targetID).Scan(&sessionsAfterLogin); err != nil {
				t.Fatal(err)
			}
			if sessionsAfterLogin != sessionsBeforeRelease {
				t.Fatalf("旧凭据插入了撤销遗漏的会话：before=%d after=%d", sessionsBeforeRelease, sessionsAfterLogin)
			}
		})
	}
}

type gatedUserRepository struct {
	delegate auth.UserRepository
	reached  chan struct{}
	release  chan struct{}
	once     sync.Once
}

func (repository *gatedUserRepository) FindByEmail(ctx context.Context, email string) (auth.User, error) {
	user, err := repository.delegate.FindByEmail(ctx, email)
	repository.once.Do(func() { close(repository.reached) })
	<-repository.release
	return user, err
}
