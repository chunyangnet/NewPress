package httpserver

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/virtualcurrency"
)

type currencyCatalogItem struct {
	Key      string              `json:"key"`
	Settings currencySettingsDTO `json:"settings"`
}

func (s *Server) currencyCatalogView(r *http.Request) ([]currencyCatalogItem, error) {
	if s.currencyCatalog == nil {
		return nil, virtualcurrency.ErrStoreUnavailable
	}
	items, err := s.currencyCatalog.List(r.Context())
	if err != nil {
		return nil, err
	}
	result := make([]currencyCatalogItem, 0, len(items))
	for _, item := range items {
		result = append(result, currencyCatalogItem{Key: item.Key, Settings: currencySettingsView(item.Settings)})
	}
	return result, nil
}

func (s *Server) adminCurrencyList(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.requireAdminJSONReadCapability(w, r, auth.CapabilitySettingsManage); !ok || s.currencyUnavailable(w, r) {
		return
	}
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	items, err := s.currencyCatalogView(r)
	if err != nil {
		s.currencyError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"default_currency": virtualcurrency.DefaultCurrencyKey, "max_currencies": virtualcurrency.MaxCurrencies, "currencies": items})
}

func (s *Server) socialWalletList(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.currencyPrincipal(w, r, false); !ok || s.currencyUnavailable(w, r) {
		return
	}
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	items, err := s.currencyCatalogView(r)
	if err != nil {
		s.currencyError(w, r, err)
		return
	}
	type choice struct {
		Key       string `json:"key"`
		Name      string `json:"name"`
		Symbol    string `json:"symbol"`
		Precision int    `json:"precision"`
		Enabled   bool   `json:"enabled"`
	}
	choices := make([]choice, 0, len(items))
	for _, item := range items {
		choices = append(choices, choice{item.Key, item.Settings.Name, item.Settings.Symbol, item.Settings.Precision, item.Settings.Enabled})
	}
	// Disabled currencies remain selectable: users still own their balances and
	// must be able to inspect history and resolve an outstanding receipt.
	writeJSON(w, 200, map[string]any{"default_currency": virtualcurrency.DefaultCurrencyKey, "currencies": choices})
}

func (s *Server) adminCurrencyCreate(w http.ResponseWriter, r *http.Request) {
	p, ok := s.currencyAdminWrite(w, r)
	if !ok || s.currencyUnavailable(w, r) {
		return
	}
	if !s.currencyRate(w, r, "currency-create:"+strconv.FormatInt(p.UserID, 10), 12) {
		return
	}
	var input struct {
		Key       string `json:"key"`
		Name      string `json:"name"`
		Symbol    string `json:"symbol"`
		Precision int    `json:"precision"`
		RequestID string `json:"request_id"`
	}
	raw, ok := s.decodeCurrencyJSON(w, r, &input)
	if !ok {
		return
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields["precision"] == nil {
		s.writeAPIError(w, r, 400, "request.invalid_json", "请填写新货币的名称、标识、单位和小数位数", nil)
		return
	}
	item, replayed, err := s.currencyCatalog.Create(r.Context(), virtualcurrency.CreateCurrency{Key: input.Key, Name: input.Name, Symbol: input.Symbol, Precision: input.Precision, ActorID: p.UserID, RequestID: input.RequestID})
	if err != nil {
		s.currencyError(w, r, err)
		return
	}
	status := http.StatusCreated
	if replayed {
		status = http.StatusOK
	} else {
		slog.Info("currency created", "actor_id", p.UserID, "currency_key", item.Key)
	}
	writeJSON(w, status, map[string]any{"currency_key": item.Key, "settings": currencySettingsView(item.Settings), "replayed": replayed})
}
