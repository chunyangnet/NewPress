package verification

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

var (
	ErrRequired    = errors.New("请先完成人机验证")
	ErrInvalid     = errors.New("验证未通过或已过期，请重新验证")
	ErrUnavailable = errors.New("验证服务暂不可用，请稍后重试或联系管理员")
)

type PublicConfig struct {
	Enabled      bool   `json:"enabled"`
	Scene        string `json:"scene"`
	Provider     string `json:"provider,omitempty"`
	SiteKey      string `json:"site_key,omitempty"`
	Theme        string `json:"theme,omitempty"`
	Language     string `json:"language,omitempty"`
	RecaptchaNet bool   `json:"recaptcha_net,omitempty"`
	Revision     string `json:"revision"`
}

type Proof struct {
	Token         string `json:"token"`
	ChallengeID   string `json:"challenge_id"`
	Answer        string `json:"answer"`
	LotNumber     string `json:"lot_number"`
	CaptchaOutput string `json:"captcha_output"`
	PassToken     string `json:"pass_token"`
	GenTime       string `json:"gen_time"`
	Randstr       string `json:"randstr"`
}

type ticket struct {
	scene, source, revision string
	expires                 time.Time
	answer                  [32]byte
	attempts                int
}
type Service struct {
	Store      *Store
	HTTP       *http.Client
	mu         sync.Mutex
	challenges map[string]ticket
	receipts   map[string]ticket
	used       map[string]time.Time
}

func NewService(store *Store) *Service {
	return &Service{Store: store, HTTP: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, challenges: map[string]ticket{}, receipts: map[string]ticket{}, used: map[string]time.Time{}}
}

func (s *Service) Config(scene string, member bool, preview string) PublicConfig {
	settings := s.Store.runtime()
	rule := settings.Scenes[scene]
	id := rule.Provider
	if id == "" {
		id = settings.DefaultProvider
	}
	enabled := settings.Enabled && rule.Enabled && !(member && rule.SkipMembers)
	if preview != "" {
		id = preview
		enabled = true
	}
	p := providerFor(settings, id)
	c := PublicConfig{Enabled: enabled, Scene: scene, Revision: settings.Revision}
	if enabled {
		c.Provider = id
		c.SiteKey = p.SiteKey
		c.RecaptchaNet = p.RecaptchaNet
		c.Theme = settings.Theme
		c.Language = settings.Language
	}
	return c
}

func (s *Service) cleanLocked(now time.Time) {
	for k, v := range s.challenges {
		if !v.expires.After(now) {
			delete(s.challenges, k)
		}
	}
	for k, v := range s.receipts {
		if !v.expires.After(now) {
			delete(s.receipts, k)
		}
	}
	for k, v := range s.used {
		if !v.After(now) {
			delete(s.used, k)
		}
	}
}
func hash(value string) string { b := sha256.Sum256([]byte(value)); return hex.EncodeToString(b[:]) }
func randomInt(max int) int {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(max)))
	if err != nil {
		panic(err)
	}
	return int(n.Int64())
}

type Challenge struct {
	ID        string `json:"id"`
	Image     string `json:"image"`
	ExpiresIn int    `json:"expires_in"`
}

func (s *Service) Challenge(c PublicConfig, source string) (Challenge, error) {
	if !c.Enabled || c.Provider != "local" {
		return Challenge{}, ErrInvalid
	}
	answer := ""
	for range 5 {
		answer += strconv.Itoa(randomInt(10))
	}
	img := image.NewRGBA(image.Rect(0, 0, 190, 60))
	for y := 0; y < 60; y++ {
		for x := 0; x < 190; x++ {
			img.Set(x, y, color.RGBA{242, 245, 250, 255})
		}
	}
	for i, ch := range answer {
		small := image.NewRGBA(image.Rect(0, 0, 8, 15))
		d := font.Drawer{Dst: small, Src: image.NewUniform(color.RGBA{uint8(25 + randomInt(40)), uint8(45 + randomInt(40)), uint8(70 + randomInt(45)), 255}), Face: basicfont.Face7x13, Dot: fixed.P(0, 12)}
		d.DrawString(string(ch))
		x0, y0 := 12+i*34+randomInt(4), 7+randomInt(7)
		for y := 0; y < 15; y++ {
			for x := 0; x < 8; x++ {
				cl := small.RGBAAt(x, y)
				if cl.A > 0 {
					for dy := 0; dy < 3; dy++ {
						for dx := 0; dx < 3; dx++ {
							img.Set(x0+x*3+dx, y0+y*3+dy, cl)
						}
					}
				}
			}
		}
	}
	for range 230 {
		img.Set(randomInt(190), randomInt(60), color.RGBA{uint8(randomInt(130)), uint8(randomInt(130)), uint8(randomInt(130)), 255})
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		return Challenge{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanLocked(time.Now())
	if len(s.challenges) >= 4096 {
		return Challenge{}, ErrUnavailable
	}
	id := randomID()
	s.challenges[hash(id)] = ticket{scene: c.Scene, source: source, revision: c.Revision, expires: time.Now().Add(2 * time.Minute), answer: sha256.Sum256([]byte(answer))}
	return Challenge{ID: id, Image: "data:image/png;base64," + base64.StdEncoding.EncodeToString(b.Bytes()), ExpiresIn: 120}, nil
}

func (s *Service) Verify(ctx context.Context, c PublicConfig, source, ip, hostname string, p Proof) (string, error) {
	if !c.Enabled || !validProvider(c.Provider) {
		return "", ErrInvalid
	}
	settings := s.Store.runtime()
	if settings.Revision != c.Revision {
		return "", ErrInvalid
	}
	if c.Provider == "local" {
		s.mu.Lock()
		key := hash(p.ChallengeID)
		t, ok := s.challenges[key]
		if !ok || t.scene != c.Scene || t.source != source || t.revision != c.Revision || !t.expires.After(time.Now()) {
			s.mu.Unlock()
			return "", ErrInvalid
		}
		answer := sha256.Sum256([]byte(strings.TrimSpace(p.Answer)))
		t.attempts++
		s.challenges[key] = t
		if subtle.ConstantTimeCompare(answer[:], t.answer[:]) != 1 {
			if t.attempts >= 5 {
				delete(s.challenges, key)
			}
			s.mu.Unlock()
			return "", ErrInvalid
		}
		delete(s.challenges, key)
		s.mu.Unlock()
	} else {
		provider := providerFor(settings, c.Provider)
		if !configured(provider) {
			return "", ErrUnavailable
		}
		// Cache the upstream proof itself, independent of scene and client, so a
		// successful upstream token cannot mint additional local receipts.
		identity := p.Token
		if c.Provider == "geetest" {
			identity = p.LotNumber
		}
		if identity == "" || len(identity) > 8192 {
			return "", ErrRequired
		}
		key := hash(c.Provider + "\x00" + provider.SiteKey + "\x00" + identity)
		s.mu.Lock()
		s.cleanLocked(time.Now())
		_, used := s.used[key]
		if used || len(s.used) >= 8192 {
			s.mu.Unlock()
			return "", ErrInvalid
		}
		s.used[key] = time.Now().Add(10 * time.Minute)
		s.mu.Unlock()
		ctx, cancel := context.WithTimeout(ctx, time.Duration(settings.TimeoutSeconds)*time.Second)
		defer cancel()
		if err := s.verifyProvider(ctx, provider, c.Scene, ip, hostname, p); err != nil {
			return "", err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanLocked(time.Now())
	if len(s.receipts) >= 4096 {
		return "", ErrUnavailable
	}
	token := randomID()
	s.receipts[hash(token)] = ticket{scene: c.Scene, source: source, revision: c.Revision, expires: time.Now().Add(2 * time.Minute)}
	return token, nil
}

func (s *Service) Consume(c PublicConfig, source, token string) error {
	if !c.Enabled {
		return nil
	}
	if token == "" {
		return ErrRequired
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := hash(token)
	t, ok := s.receipts[key]
	if !ok || t.scene != c.Scene || t.source != source || t.revision != c.Revision || !t.expires.After(time.Now()) {
		return ErrInvalid
	}
	delete(s.receipts, key)
	return nil
}

func (s *Service) post(ctx context.Context, endpoint string, values url.Values, target any) error {
	r, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return ErrUnavailable
	}
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return s.request(r, target)
}
func (s *Service) request(r *http.Request, target any) error {
	resp, err := s.HTTP.Do(r)
	if err != nil {
		return ErrUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return ErrUnavailable
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 65537))
	if err != nil || len(b) > 65536 || json.Unmarshal(b, target) != nil {
		return ErrUnavailable
	}
	return nil
}
func mac(key []byte, value string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(value))
	return m.Sum(nil)
}

func (s *Service) verifyProvider(ctx context.Context, p Provider, scene, ip, hostname string, proof Proof) error {
	if p.ID == "geetest" {
		if proof.LotNumber == "" || proof.CaptchaOutput == "" || proof.PassToken == "" || proof.GenTime == "" {
			return ErrRequired
		}
		var result struct {
			Result string `json:"result"`
		}
		values := url.Values{"captcha_id": {p.SiteKey}, "lot_number": {proof.LotNumber}, "captcha_output": {proof.CaptchaOutput}, "pass_token": {proof.PassToken}, "gen_time": {proof.GenTime}, "sign_token": {hex.EncodeToString(mac([]byte(p.Secret), proof.LotNumber))}}
		if err := s.post(ctx, "https://gcaptcha4.geetest.com/validate?captcha_id="+url.QueryEscape(p.SiteKey), values, &result); err != nil {
			return err
		}
		if result.Result != "success" {
			return ErrInvalid
		}
		return nil
	}
	if p.ID == "tencent" {
		return s.verifyTencent(ctx, p, ip, proof)
	}
	endpoint := "https://challenges.cloudflare.com/turnstile/v0/siteverify"
	values := url.Values{"secret": {p.Secret}, "response": {proof.Token}, "remoteip": {ip}}
	if p.ID == "hcaptcha" {
		endpoint = "https://api.hcaptcha.com/siteverify"
		values.Set("sitekey", p.SiteKey)
	}
	if strings.HasPrefix(p.ID, "recaptcha_") {
		domain := "www.google.com"
		if p.RecaptchaNet {
			domain = "www.recaptcha.net"
		}
		endpoint = "https://" + domain + "/recaptcha/api/siteverify"
	}
	var result struct {
		Success  bool     `json:"success"`
		Hostname string   `json:"hostname"`
		Action   string   `json:"action"`
		Score    *float64 `json:"score"`
	}
	if err := s.post(ctx, endpoint, values, &result); err != nil {
		return err
	}
	if !result.Success || hostname == "" || !strings.EqualFold(strings.TrimSuffix(result.Hostname, "."), strings.TrimSuffix(hostname, ".")) {
		return ErrInvalid
	}
	if (p.ID == "turnstile" || p.ID == "recaptcha_v3") && result.Action != scene {
		return ErrInvalid
	}
	if p.ID == "recaptcha_v3" && (result.Score == nil || *result.Score < p.MinScore) {
		return ErrInvalid
	}
	return nil
}

func (s *Service) verifyTencent(ctx context.Context, p Provider, ip string, proof Proof) error {
	appID, err := strconv.ParseInt(p.SiteKey, 10, 64)
	if err != nil || appID <= 0 || proof.Randstr == "" || strings.HasPrefix(proof.Token, "trerror") {
		return ErrInvalid
	}
	body, _ := json.Marshal(map[string]any{"CaptchaType": 9, "Ticket": proof.Token, "UserIp": ip, "Randstr": proof.Randstr, "CaptchaAppId": appID, "AppSecretKey": p.Secret})
	r, err := http.NewRequestWithContext(ctx, "POST", "https://captcha.tencentcloudapi.com", bytes.NewReader(body))
	if err != nil {
		return ErrUnavailable
	}
	now := time.Now().UTC()
	date := now.Format("2006-01-02")
	stamp := strconv.FormatInt(now.Unix(), 10)
	canonical := "POST\n/\n\ncontent-type:application/json; charset=utf-8\nhost:captcha.tencentcloudapi.com\n\ncontent-type;host\n" + hash(string(body))
	scope := date + "/captcha/tc3_request"
	toSign := "TC3-HMAC-SHA256\n" + stamp + "\n" + scope + "\n" + hash(canonical)
	key := mac(mac(mac([]byte("TC3"+p.APISecret), date), "captcha"), "tc3_request")
	r.Header.Set("Authorization", "TC3-HMAC-SHA256 Credential="+p.SecretID+"/"+scope+", SignedHeaders=content-type;host, Signature="+hex.EncodeToString(mac(key, toSign)))
	r.Header.Set("Content-Type", "application/json; charset=utf-8")
	r.Header.Set("X-TC-Action", "DescribeCaptchaResult")
	r.Header.Set("X-TC-Version", "2019-07-22")
	r.Header.Set("X-TC-Timestamp", stamp)
	var result struct {
		Response struct {
			CaptchaCode int              `json:"CaptchaCode"`
			Error       *json.RawMessage `json:"Error"`
		} `json:"Response"`
	}
	if err := s.request(r, &result); err != nil {
		return err
	}
	if result.Response.Error != nil {
		return ErrUnavailable
	}
	if result.Response.CaptchaCode != 1 {
		return ErrInvalid
	}
	return nil
}
