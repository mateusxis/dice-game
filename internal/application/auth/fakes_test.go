package auth_test

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mateusxis/cassino/internal/application/ports"
	"github.com/mateusxis/cassino/internal/domain/player"
)

// fakePlayerRepo is a minimal in-memory ports.PlayerRepository for use case
// tests; it does not attempt to model row locking.
type fakePlayerRepo struct {
	byEmail   map[string]*player.Player
	byID      map[string]*player.Player
	createErr error
}

var _ ports.PlayerRepository = (*fakePlayerRepo)(nil)

func newFakePlayerRepo() *fakePlayerRepo {
	return &fakePlayerRepo{byEmail: map[string]*player.Player{}, byID: map[string]*player.Player{}}
}

func (f *fakePlayerRepo) Create(_ context.Context, p *player.Player) error {
	if f.createErr != nil {
		return f.createErr
	}
	if _, ok := f.byEmail[p.Email]; ok {
		return player.ErrEmailAlreadyUsed
	}
	cp := *p
	f.byEmail[p.Email] = &cp
	f.byID[p.ID] = &cp
	return nil
}

func (f *fakePlayerRepo) GetByID(_ context.Context, id string) (*player.Player, error) {
	p, ok := f.byID[id]
	if !ok {
		return nil, player.ErrNotFound
	}
	cp := *p
	return &cp, nil
}

func (f *fakePlayerRepo) GetByEmail(_ context.Context, email string) (*player.Player, error) {
	p, ok := f.byEmail[email]
	if !ok {
		return nil, player.ErrNotFound
	}
	cp := *p
	return &cp, nil
}

func (f *fakePlayerRepo) GetForUpdate(ctx context.Context, id string) (*player.Player, error) {
	return f.GetByID(ctx, id)
}

func (f *fakePlayerRepo) UpdateBalance(_ context.Context, id string, balance int64) error {
	p, ok := f.byID[id]
	if !ok {
		return player.ErrNotFound
	}
	p.Balance = balance
	return nil
}

// fakeHasher is a deterministic, non-cryptographic stand-in for
// ports.PasswordHasher.
type fakeHasher struct {
	hashErr    error
	compareErr error
}

var _ ports.PasswordHasher = (*fakeHasher)(nil)

func (f *fakeHasher) Hash(plain string) (string, error) {
	if f.hashErr != nil {
		return "", f.hashErr
	}
	return "hashed:" + plain, nil
}

func (f *fakeHasher) Compare(hash, plain string) error {
	if f.compareErr != nil {
		return f.compareErr
	}
	if hash != "hashed:"+plain {
		return player.ErrInvalidCredentials
	}
	return nil
}

// fakeIDGen hands out predictable, incrementing ids.
type fakeIDGen struct{ n int }

var _ ports.IDGenerator = (*fakeIDGen)(nil)

func (f *fakeIDGen) NewID() string {
	f.n++
	return fmt.Sprintf("id-%d", f.n)
}

// fakeTokenService is a minimal ports.TokenService; Verify is unused by the
// auth use cases and always errors.
type fakeTokenService struct {
	issueErr  error
	token     string
	expiresAt time.Time
}

var _ ports.TokenService = (*fakeTokenService)(nil)

func (f *fakeTokenService) Issue(playerID, _ string) (string, time.Time, error) {
	if f.issueErr != nil {
		return "", time.Time{}, f.issueErr
	}
	tok := f.token
	if tok == "" {
		tok = "token-for-" + playerID
	}
	exp := f.expiresAt
	if exp.IsZero() {
		exp = time.Now().Add(time.Hour)
	}
	return tok, exp, nil
}

func (f *fakeTokenService) Verify(string) (ports.Claims, error) {
	return ports.Claims{}, errors.New("fakeTokenService: Verify not used by auth use cases")
}
