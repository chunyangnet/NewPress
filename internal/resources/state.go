package resources

import (
	"context"
	"net/mail"
	"strings"
	"time"
)

type Submission struct {
	Actor        string            `json:"actor"`
	PostID       int64             `json:"post_id"`
	Resource     string            `json:"resource"`
	Variant      string            `json:"variant"`
	Passed       bool              `json:"passed"`
	FormComplete bool              `json:"form_complete"`
	Fields       map[string]string `json:"fields"`
	Day          string            `json:"day"`
	Attempts     int               `json:"attempts"`
	UpdatedAt    time.Time         `json:"updated_at"`
}
type Delivery struct {
	Actor     string    `json:"actor"`
	PostID    int64     `json:"post_id"`
	Resource  string    `json:"resource"`
	Variant   string    `json:"variant"`
	Link      string    `json:"link"`
	Request   string    `json:"request"`
	Title     string    `json:"title"`
	Day       string    `json:"day"`
	CreatedAt time.Time `json:"created_at"`
}
type State struct {
	Schema      int                      `json:"schema"`
	Submissions map[string]Submission    `json:"submissions"`
	Deliveries  []Delivery               `json:"deliveries"`
	Unlocks     []Unlock                 `json:"unlocks,omitempty"`
	Purchases   map[string]PointPurchase `json:"purchases,omitempty"`
}

type Unlock struct {
	Actor     string    `json:"actor"`
	PostID    int64     `json:"post_id"`
	Resource  string    `json:"resource"`
	Variant   string    `json:"variant"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Store) RecordUnlock(ctx context.Context, u Unlock) error {
	return s.Backend.Update(ctx, func(st *State) error {
		for _, old := range st.Unlocks {
			if old.Actor == u.Actor && old.PostID == u.PostID && old.Resource == u.Resource && old.Variant == u.Variant {
				return nil
			}
		}
		st.Unlocks = append(st.Unlocks, u)
		return nil
	})
}

func InitialState() State {
	return State{Schema: 1, Submissions: map[string]Submission{}, Deliveries: []Delivery{}}
}
func ValidateState(s State) error {
	if s.Schema != 1 || s.Submissions == nil || len(s.Submissions) > 100000 || len(s.Deliveries) > 200000 || len(s.Unlocks) > 100000 || len(s.Purchases) > 100000 {
		return ErrUnavailable
	}
	for k, v := range s.Submissions {
		if len(k) != 64 || v.Actor == "" || v.PostID < 1 || !ValidID(v.Resource) || !ValidID(v.Variant) || v.Attempts < 0 || len(v.Fields) > 20 || v.UpdatedAt.IsZero() {
			return ErrInvalid
		}
	}
	for _, v := range s.Deliveries {
		if v.Actor == "" || v.PostID < 1 || !ValidID(v.Resource) || !ValidID(v.Variant) || !ValidID(v.Link) || !ValidID(v.Request) || v.CreatedAt.IsZero() {
			return ErrInvalid
		}
	}
	for _, u := range s.Unlocks {
		if u.Actor == "" || u.PostID < 1 || !ValidID(u.Resource) || !ValidID(u.Variant) || u.CreatedAt.IsZero() {
			return ErrInvalid
		}
	}
	for key, p := range s.Purchases {
		if key != p.Key() || !p.Valid() {
			return ErrInvalid
		}
	}
	return nil
}

type Backend interface {
	Read(context.Context) (State, error)
	Update(context.Context, func(*State) error) error
}
type Store struct{ Backend Backend }

func Day(now time.Time, zone string) string {
	l, e := time.LoadLocation(zone)
	if e != nil {
		l = time.UTC
	}
	return now.In(l).Format("2006-01-02")
}
func Counts(st State, actor string, post int64, id, day string) (daily, total int) {
	for _, d := range st.Deliveries {
		if d.Actor == actor && d.PostID == post && d.Resource == id {
			total++
			if d.Day == day {
				daily++
			}
		}
	}
	return
}
func Remaining(r Resource, daily, total int) int {
	n := -1
	if r.DailyLimit > 0 {
		n = max(0, r.DailyLimit-daily)
	}
	if r.TotalLimit > 0 && (n < 0 || r.TotalLimit-total < n) {
		n = max(0, r.TotalLimit-total)
	}
	return n
}

// Consume checks limits and inserts the receipt under one backend transaction.
// An identical retry does not consume another download; a reused key cannot select a different file.
func (s *Store) Consume(ctx context.Context, d Delivery, r Resource) (bool, error) {
	replay := false
	err := s.Backend.Update(ctx, func(st *State) error {
		for _, old := range st.Deliveries {
			if old.Actor == d.Actor && old.Request == d.Request {
				if old.PostID != d.PostID || old.Resource != d.Resource || old.Variant != d.Variant || old.Link != d.Link {
					return ErrInvalid
				}
				replay = true
				return nil
			}
		}
		daily, total := Counts(*st, d.Actor, d.PostID, r.ID, d.Day)
		if Remaining(r, daily, total) == 0 {
			return ErrLimit
		}
		st.Deliveries = append(st.Deliveries, d)
		return nil
	})
	return replay, err
}
func (s *Store) Submit(ctx context.Context, actor string, post int64, r Resource, v Variant, fields, answers map[string]string, day string) (Submission, error) {
	var result Submission
	if len(fields) > 20 || len(answers) > 20 {
		return result, ErrInvalid
	}
	for k, a := range answers {
		found := false
		for _, q := range v.Questions {
			if q.ID == k {
				found = true
			}
		}
		if !found || len(a) > 500 {
			return result, ErrInvalid
		}
	}
	for k, a := range fields {
		found := false
		for _, f := range v.Fields {
			if f.ID == k {
				found = true
			}
		}
		if !found || len(a) > 4000 {
			return result, ErrInvalid
		}
	}
	form := len(v.Fields) > 0 && fields != nil
	if form {
		for _, f := range v.Fields {
			value := strings.TrimSpace(fields[f.ID])
			if len(value) > 4000 || f.Required && (value == "" || f.Type == "checkbox" && value != "true") {
				return result, ErrInvalid
			}
			if value != "" {
				switch f.Type {
				case "email":
					a, e := mail.ParseAddress(value)
					if e != nil || a.Address != value {
						return result, ErrInvalid
					}
				case "select":
					if !Contains(f.Options, value) {
						return result, ErrInvalid
					}
				case "checkbox":
					if value != "true" && value != "false" {
						return result, ErrInvalid
					}
				}
			}
		}
	}
	err := s.Backend.Update(ctx, func(st *State) error {
		key := SubmissionKey(actor, post, r, v)
		previous := st.Submissions[key]
		result = previous
		result.Actor, result.PostID, result.Resource, result.Variant = actor, post, r.ID, v.ID
		if result.Day != day {
			result.Day = day
			result.Attempts = 0
		}
		if len(v.Questions) > 0 && answers != nil && !result.Passed {
			if result.Attempts >= v.AttemptsPerDay {
				return ErrLimit
			}
			result.Attempts++
			correct := 0
			for _, q := range v.Questions {
				if strings.EqualFold(strings.TrimSpace(answers[q.ID]), strings.TrimSpace(q.Answer)) {
					correct++
				}
			}
			result.Passed = correct*100 >= len(v.Questions)*v.PassPercent
		}
		if form {
			result.FormComplete = true
			result.Fields = fields
		}
		result.UpdatedAt = time.Now().UTC()
		st.Submissions[key] = result
		return nil
	})
	return result, err
}

type Check struct {
	ID             string `json:"id"`
	Kind           string `json:"kind"`
	Label          string `json:"label"`
	Passed         bool   `json:"passed"`
	Amount         int64  `json:"amount"`
	Currency       string `json:"currency"`
	Purchasable    bool   `json:"purchasable"`
	PurchaseReason string `json:"purchase_reason,omitempty"`
	OrderID        string `json:"order_id,omitempty"`
	OrderStatus    string `json:"order_status,omitempty"`
	OrderAmount    string `json:"order_amount,omitempty"`
	OrderCurrency  string `json:"order_currency,omitempty"`
}
type GroupStatus struct {
	Name       string  `json:"name"`
	Passed     bool    `json:"passed"`
	Conditions []Check `json:"conditions"`
}

func Evaluate(v Variant, check func(Condition) bool) (bool, []GroupStatus) {
	allowed := false
	groups := []GroupStatus{}
	for _, g := range v.Groups {
		out := GroupStatus{Name: g.Name, Passed: len(g.Conditions) > 0, Conditions: []Check{}}
		for _, c := range g.Conditions {
			ok := check(c)
			out.Conditions = append(out.Conditions, Check{ID: c.ID, Kind: c.Kind, Label: Label(c), Passed: ok, Amount: c.Amount, Currency: c.Currency})
			out.Passed = out.Passed && ok
		}
		allowed = allowed || out.Passed
		groups = append(groups, out)
	}
	return allowed, groups
}
