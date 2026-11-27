package database

import (
	"context"
	"sync"
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/recognition"
)

type recognitionPost struct {
	content.Repository
	author int64
}

func (p recognitionPost) GetByID(_ context.Context, id int64) (content.Post, error) {
	return content.Post{ID: id, AuthorID: p.author, Type: content.TypePost, Status: content.StatusPublish, PublishedAt: time.Now().Add(-time.Hour)}, nil
}

func TestPostgreSQLRecognitionConcurrentTipsAndAwards(t *testing.T) {
	coin, cfg, ids := currencyPostgres(t)
	backend := NewRecognitionBackend(coin.pool)
	service := &recognition.Service{Backend: backend, Users: NewUserRepository(coin.pool), Currencies: NewCurrencyCatalog(coin.pool), Content: recognitionPost{author: ids[0]}}
	settings := recognition.InitialState().Settings
	settings.TipsEnabled = true
	settings.VerificationEnabled = true
	settings.Currencies[0].Presets = []string{"1.25"}
	if err := service.SaveSettings(t.Context(), settings); err != nil {
		t.Fatal(err)
	}
	currencyPGSeed(t, coin, cfg, ids[0], ids[1], 10000)
	in := recognition.TipInput{PostID: 7, Currency: "main", Amount: "1.25", RequestID: "postgres-recognition-tip", SettingsVersion: 2, CurrencyVersion: cfg.Version}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			other := *service
			other.Backend = NewRecognitionBackend(coin.pool)
			if _, err := other.Tip(t.Context(), ids[1], in); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	balance, err := coin.Balance(t.Context(), ids[1])
	if err != nil || balance != 9875 {
		t.Fatal(balance, err)
	}
	identity, err := service.UpdateIdentity(t.Context(), ids[0], true, recognition.IdentityInput{UserID: ids[1], Version: 1, Action: "grant", Key: "original", Reason: "优秀原创", Days: 30})
	if err != nil {
		t.Fatal(err)
	}
	state, err := NewRecognitionBackend(coin.pool).Read(t.Context())
	if err != nil || state.Identity(ids[1]).Version != identity.Version || len(state.PublicBadges(ids[1], time.Now())) != 1 {
		t.Fatal(state, err)
	}
	if err = service.Ready(t.Context()); err != nil {
		t.Fatal(err)
	}
}
