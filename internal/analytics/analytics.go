// Package analytics owns bounded daily aggregates. It never stores visitor
// identifiers, cookies, IP addresses, user agents, or complete referrer URLs.
package analytics

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"newpress.local/newpress/internal/packagefs"
)

const (
	RetentionDays          = 90
	MaxPosts               = 1000
	MaxSources             = 100
	MaxTerms               = 100
	MaxWriters             = 256
	MaxDocumentBytes       = 16 << 20
	MaxCount         int64 = 1_000_000_000_000
	Other                  = "(other)"
)

var (
	ErrInvalid  = errors.New("invalid analytics data")
	ErrCapacity = errors.New("analytics capacity reached")
	ErrChanged  = errors.New("analytics file changed externally")
	digits      = regexp.MustCompile(`[0-9]{7,}`)
	hexID       = regexp.MustCompile(`^[a-f0-9]{32}$`)
)

type Day struct {
	Date     string           `json:"date"`
	Views    int64            `json:"views"`
	Searches int64            `json:"searches"`
	Misses   int64            `json:"misses"`
	Posts    map[int64]int64  `json:"posts"`
	Sources  map[string]int64 `json:"sources"`
	Terms    map[string]int64 `json:"terms"`
}
type Receipt struct {
	Sequence  int64     `json:"sequence"`
	Hash      string    `json:"hash"`
	UpdatedAt time.Time `json:"updated_at"`
}
type State struct {
	Schema    int                `json:"schema"`
	Days      map[string]Day     `json:"days"`
	Writers   map[string]Receipt `json:"writers"`
	UpdatedAt time.Time          `json:"updated_at"`
}
type Batch struct {
	Writer   string         `json:"writer"`
	Sequence int64          `json:"sequence"`
	Days     map[string]Day `json:"days"`
}
type Store interface {
	Apply(context.Context, Batch, time.Time) error
	Snapshot(context.Context, time.Time) (State, error)
}
type Event struct {
	At     time.Time
	PostID int64
	Source string
	Search bool
	Miss   bool
	Term   string
}

type locationKey struct{}

// WithLocation freezes the calendar used by an analytics operation. A nil
// location explicitly selects UTC, rather than a store's configured callback.
// The location is operation metadata only; it is never persisted in a batch.
func WithLocation(ctx context.Context, loc *time.Location) context.Context {
	if loc == nil {
		loc = time.UTC
	}
	return context.WithValue(ctx, locationKey{}, loc)
}

// LocationFromContext returns the calendar frozen by WithLocation, if any.
// Store implementations use it before consulting their configured callback.
func LocationFromContext(ctx context.Context) (*time.Location, bool) {
	loc, ok := ctx.Value(locationKey{}).(*time.Location)
	return loc, ok
}

func operationLocation(ctx context.Context, fallback func() *time.Location) *time.Location {
	if loc, ok := LocationFromContext(ctx); ok {
		return loc
	}
	return fallback()
}

func EmptyState() State {
	return State{Schema: 1, Days: map[string]Day{}, Writers: map[string]Receipt{}}
}
func NewDay(date string) Day {
	return Day{Date: date, Posts: map[int64]int64{}, Sources: map[string]int64{}, Terms: map[string]int64{}}
}

// Date keeps a UTC calendar label for callers that have no site settings.
// Serving code uses DateIn with 站点设置 timezone so daily buckets follow the
// same calendar day the operators and readers see.
func Date(now time.Time) string { return DateIn(now, time.UTC) }

// DateIn labels a calendar day in location. A changed site timezone shifts the
// boundary of future buckets only; stored labels are never rewritten.
func DateIn(now time.Time, location *time.Location) string {
	if location == nil {
		location = time.UTC
	}
	return now.In(location).Format(time.DateOnly)
}
func cutoffIn(now time.Time, location *time.Location) string {
	if location == nil {
		location = time.UTC
	}
	return now.In(location).AddDate(0, 0, -RetentionDays+1).Format(time.DateOnly)
}
func add(a, b int64) int64 { return min(MaxCount, a+b) }
func count[K comparable](m map[K]int64, key K, n int64, capacity int, other K) {
	if _, exists := m[key]; !exists && key != other && len(m) >= capacity {
		key = other
	}
	m[key] = add(m[key], n)
}
func AddEvent(days map[string]Day, event Event) {
	AddEventIn(days, event, time.UTC)
}

// AddEventIn buckets an event by its calendar day in location.
func AddEventIn(days map[string]Day, event Event, location *time.Location) {
	date := DateIn(event.At, location)
	d, ok := days[date]
	if !ok {
		d = NewDay(date)
	}
	d.Views = add(d.Views, 1)
	if event.PostID > 0 {
		count(d.Posts, event.PostID, 1, MaxPosts, int64(0))
	}
	count(d.Sources, event.Source, 1, MaxSources, Other)
	if event.Search {
		d.Searches = add(d.Searches, 1)
		if event.Miss {
			d.Misses = add(d.Misses, 1)
			if event.Term != "" {
				count(d.Terms, event.Term, 1, MaxTerms, Other)
			}
		}
	}
	days[date] = d
}
func Clone(s State) State {
	d := EmptyState()
	d.UpdatedAt = s.UpdatedAt
	for date, value := range s.Days {
		next := NewDay(date)
		next.Views, next.Searches, next.Misses = value.Views, value.Searches, value.Misses
		for k, n := range value.Posts {
			next.Posts[k] = n
		}
		for k, n := range value.Sources {
			next.Sources[k] = n
		}
		for k, n := range value.Terms {
			next.Terms[k] = n
		}
		d.Days[date] = next
	}
	for key, value := range s.Writers {
		d.Writers[key] = value
	}
	return d
}
func Prune(s *State, now time.Time) { PruneIn(s, now, time.UTC) }

// PruneIn expires days and writer receipts older than the retention window,
// measured on the calendar of location. Future labels from a previous timezone
// are retained unchanged; if necessary the oldest labels are evicted to keep
// the document within RetentionDays buckets.
func PruneIn(s *State, now time.Time, location *time.Location) {
	old := cutoffIn(now, location)
	for date := range s.Days {
		if date < old {
			delete(s.Days, date)
		}
	}
	trimDays(s)
	for key, r := range s.Writers {
		if DateIn(r.UpdatedAt, location) < old {
			delete(s.Writers, key)
		}
	}
}

func trimDays(s *State) {
	if excess := len(s.Days) - RetentionDays; excess > 0 {
		dates := make([]string, 0, len(s.Days))
		for date := range s.Days {
			dates = append(dates, date)
		}
		sort.Strings(dates)
		for _, date := range dates[:excess] {
			delete(s.Days, date)
		}
	}
}
func validDay(d Day) bool {
	parsed, err := time.Parse(time.DateOnly, d.Date)
	if err != nil || Date(parsed) != d.Date || d.Views < 0 || d.Views > MaxCount || d.Searches < 0 || d.Searches > d.Views || d.Misses < 0 || d.Misses > d.Searches || d.Posts == nil || d.Sources == nil || d.Terms == nil || len(d.Posts) > MaxPosts+1 || len(d.Sources) > MaxSources+1 || len(d.Terms) > MaxTerms+1 {
		return false
	}
	var posts, sources, terms int64
	for k, n := range d.Posts {
		if k < 0 || n <= 0 || n > d.Views {
			return false
		}
		posts += n
	}
	for k, n := range d.Sources {
		if !ValidSource(k) || n <= 0 || n > d.Views {
			return false
		}
		sources += n
	}
	for k, n := range d.Terms {
		if k != Other && (SearchTerm(k) != k || k == "") || n <= 0 || n > d.Misses {
			return false
		}
		terms += n
	}
	return posts <= d.Views && sources == d.Views && terms <= d.Misses
}
func Validate(s State) error {
	if s.Schema != 1 || s.Days == nil || s.Writers == nil || len(s.Days) > RetentionDays || len(s.Writers) > MaxWriters {
		return ErrInvalid
	}
	for key, d := range s.Days {
		if key != d.Date || !validDay(d) {
			return ErrInvalid
		}
	}
	for key, r := range s.Writers {
		hash, err := hex.DecodeString(r.Hash)
		if !hexID.MatchString(key) || r.Sequence <= 0 || r.Sequence > MaxCount || err != nil || len(hash) != 32 || r.UpdatedAt.IsZero() {
			return ErrInvalid
		}
	}
	return nil
}

// Merge rejects altered replays and gaps; a retry after an uncertain commit
// acknowledges the original batch without incrementing its counters twice.
func Merge(state State, batch Batch, now time.Time) (State, bool, error) {
	return MergeIn(state, batch, now, time.UTC)
}

// MergeIn is Merge with daily boundaries and pruning in location.
func MergeIn(state State, batch Batch, now time.Time, location *time.Location) (State, bool, error) {
	if err := Validate(state); err != nil {
		return State{}, false, err
	}
	if !hexID.MatchString(batch.Writer) || batch.Sequence <= 0 || batch.Sequence > MaxCount || len(batch.Days) > RetentionDays {
		return State{}, false, ErrInvalid
	}
	for key, d := range batch.Days {
		if key != d.Date || !validDay(d) {
			return State{}, false, ErrInvalid
		}
	}
	body, _ := json.Marshal(batch)
	hash := sha256.Sum256(body)
	digest := hex.EncodeToString(hash[:])
	next := Clone(state)
	PruneIn(&next, now, location)
	// A committed receipt proves the original calendar validation succeeded.
	// Check it before applying today's timezone to the batch's frozen labels,
	// and before expiry removes the receipt from the working copy.
	if previous, exists := state.Writers[batch.Writer]; exists && batch.Sequence == previous.Sequence {
		if digest != previous.Hash {
			return State{}, false, ErrInvalid
		}
		return next, false, nil
	}
	for date := range batch.Days {
		if date > DateIn(now, location) {
			return State{}, false, ErrInvalid
		}
	}
	if previous, exists := next.Writers[batch.Writer]; exists {
		if batch.Sequence != previous.Sequence+1 {
			return State{}, false, ErrInvalid
		}
	} else if len(next.Writers) >= MaxWriters {
		return State{}, false, ErrCapacity
	}
	for date, incoming := range batch.Days {
		if date < cutoffIn(now, location) {
			continue
		}
		d, exists := next.Days[date]
		if !exists {
			d = NewDay(date)
		}
		// A day cannot saturate partially: reject the whole batch instead.
		if incoming.Views > MaxCount-d.Views {
			return State{}, false, ErrCapacity
		}
		d.Views += incoming.Views
		d.Searches += incoming.Searches
		d.Misses += incoming.Misses
		// Sorted insertion makes overflow membership independent of map order.
		ids := make([]int64, 0, len(incoming.Posts))
		for k := range incoming.Posts {
			ids = append(ids, k)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		for _, k := range ids {
			count(d.Posts, k, incoming.Posts[k], MaxPosts, int64(0))
		}
		for _, pair := range []struct {
			src, dst map[string]int64
			limit    int
		}{{incoming.Sources, d.Sources, MaxSources}, {incoming.Terms, d.Terms, MaxTerms}} {
			keys := make([]string, 0, len(pair.src))
			for k := range pair.src {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				count(pair.dst, k, pair.src[k], pair.limit, Other)
			}
		}
		next.Days[date] = d
	}
	trimDays(&next)
	next.Writers[batch.Writer] = Receipt{batch.Sequence, digest, now.UTC()}
	next.UpdatedAt = now.UTC()
	if err := Validate(next); err != nil {
		return State{}, false, err
	}
	return next, true, nil
}

// SearchTerm deliberately omits queries resembling addresses, URLs, long
// numeric identifiers or control text. It is not a general PII detector.
func SearchTerm(raw string) string {
	if !utf8.ValidString(raw) || len(raw) > 512 {
		return ""
	}
	value := strings.ToLower(strings.Join(strings.Fields(raw), " "))
	if value == "" || value == Other || utf8.RuneCountInString(value) > 80 || strings.ContainsAny(value, "@:/\\?=&%#") || digits.MatchString(value) {
		return ""
	}
	for _, r := range raw {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ""
		}
	}
	return value
}
func ValidSource(value string) bool {
	if value == "direct" || value == "internal" || value == Other {
		return true
	}
	if len(value) > 253 || value == "" || value != strings.ToLower(value) || net.ParseIP(value) != nil || !strings.Contains(value, ".") {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}
func Source(referrer, publicURL string) string {
	if len(referrer) > 4096 {
		return Other
	}
	u, err := url.Parse(referrer)
	if err != nil || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "direct"
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	base, _ := url.Parse(publicURL)
	if base != nil && strings.EqualFold(strings.TrimSuffix(base.Hostname(), "."), host) {
		return "internal"
	}
	if !ValidSource(host) {
		return Other
	}
	return host
}

func Decode(body []byte) (State, error) {
	if len(body) > MaxDocumentBytes || !utf8.Valid(body) {
		return State{}, ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := uniqueValue(decoder, 0); err != nil {
		return State{}, err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return State{}, ErrInvalid
	}
	var s State
	// Keep the analytics-specific depth guard above, then reject keys that
	// differ in spelling but write to the same field or integer post ID.
	if err := packagefs.ValidateUniqueJSONFor(body, &s); err != nil {
		return State{}, err
	}
	decoder = json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&s); err != nil {
		return State{}, err
	}
	return s, Validate(s)
}
func uniqueValue(d *json.Decoder, depth int) error {
	if depth > 8 {
		return ErrInvalid
	}
	token, err := d.Token()
	if err != nil {
		return err
	}
	if delim, ok := token.(json.Delim); ok {
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return ErrInvalid
				}
				seen[name] = true
				if err := uniqueValue(d, depth+1); err != nil {
					return err
				}
			}
			end, err := d.Token()
			if err != nil || end != json.Delim('}') {
				return ErrInvalid
			}
		case '[':
			for d.More() {
				if err := uniqueValue(d, depth+1); err != nil {
					return err
				}
			}
			end, err := d.Token()
			if err != nil || end != json.Delim(']') {
				return ErrInvalid
			}
		default:
			return ErrInvalid
		}
	}
	return nil
}
