package cdn

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"newpress.local/newpress/internal/safehttp"
)

type OperationError struct {
	Message string
	Retry   bool
	After   time.Duration
}

func (e *OperationError) Error() string { return e.Message }
func PublicError(e error) string {
	var op *OperationError
	if errors.As(e, &op) {
		return op.Message
	}
	if errors.Is(e, context.Canceled) {
		return "操作已取消；已提交到云端的任务不能撤回"
	}
	if errors.Is(e, context.DeadlineExceeded) {
		return "请求超时，请查看任务记录后重试"
	}
	return "请求失败，请检查配置、网络和服务商权限"
}

type Receipt struct {
	ID      string            `json:"id,omitempty"`
	Message string            `json:"message"`
	Details map[string]string `json:"details,omitempty"`
}
type ProviderClient struct {
	client *http.Client
	now    func() time.Time
}

func newProviderClient() *ProviderClient {
	return &ProviderClient{safehttp.Client(25 * time.Second), time.Now}
}

var codePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`)

func remoteError(status int, code string, after time.Duration) error {
	message := fmt.Sprintf("服务商拒绝请求（HTTP %d）", status)
	if codePattern.MatchString(code) {
		message += "：" + code
	}
	retry := status == 429 || status >= 500 || strings.Contains(code, "Frequency") || strings.Contains(code, "RequestLimit") || strings.Contains(code, "OperationTooOften") || strings.HasPrefix(code, "InternalError")
	return &OperationError{message, retry, after}
}
func (a *ProviderClient) send(req *http.Request) ([]byte, error) {
	resp, e := a.client.Do(req)
	if e != nil {
		if req.Context().Err() != nil {
			return nil, req.Context().Err()
		}
		return nil, &OperationError{"云端连接失败或超时；结果可能尚未确认", true, 0}
	}
	defer resp.Body.Close()
	b, e := io.ReadAll(io.LimitReader(resp.Body, 2<<20+1))
	if e != nil {
		return nil, &OperationError{"云端响应读取失败；结果可能尚未确认", true, 0}
	}
	if len(b) > 2<<20 {
		return nil, &OperationError{"云端响应超过 2 MiB 限制", false, 0}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		delay := time.Duration(0)
		if n, e := strconv.Atoi(resp.Header.Get("Retry-After")); e == nil {
			delay = time.Duration(min(max(n, 0), 3600)) * time.Second
		} else if t, e := http.ParseTime(resp.Header.Get("Retry-After")); e == nil {
			delay = min(max(t.Sub(a.now()), 0), time.Hour)
		}
		var v struct {
			Code string `json:"Code"`
		}
		_ = json.Unmarshal(b, &v)
		return nil, remoteError(resp.StatusCode, v.Code, delay)
	}
	return b, nil
}
func request(ctx context.Context, method, endpoint string, body []byte, contentType string) *http.Request {
	r, _ := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	r.Header.Set("User-Agent", "NewPress-CDN/1.0")
	return r
}
func jsonBody(v any) []byte { b, _ := json.Marshal(v); return b }
func mac(newHash func() hash.Hash, key []byte, text string) []byte {
	h := hmac.New(newHash, key)
	_, _ = h.Write([]byte(text))
	return h.Sum(nil)
}
func digest(v []byte) string { h := sha256.Sum256(v); return hex.EncodeToString(h[:]) }

func (a *ProviderClient) Execute(ctx context.Context, p Profile, j Job) (Receipt, error) {
	switch p.Provider {
	case "cloudflare":
		return a.cloudflare(ctx, p, j)
	case "tencent", "edgeone":
		return a.tencent(ctx, p, j)
	case "aliyun":
		return a.aliyun(ctx, p, j)
	case "qiniu":
		return a.qiniu(ctx, p, j)
	case "cloudfront":
		return a.cloudfront(ctx, p, j)
	}
	return Receipt{}, errors.New("unknown CDN provider")
}
func accepted(id string) Receipt {
	if len(id) > 512 {
		id = ""
	}
	return Receipt{ID: id, Message: "服务商已受理；不代表全球节点已经完成刷新或预热"}
}
func tested() Receipt {
	return Receipt{Message: "只读接口验证通过；刷新、预热权限以实际任务结果为准"}
}

func (a *ProviderClient) cloudflare(ctx context.Context, p Profile, j Job) (Receipt, error) {
	endpoint := "https://api.cloudflare.com/client/v4/zones/" + p.ResourceID
	method := http.MethodGet
	var body []byte
	if j.Operation != "test" {
		endpoint += "/purge_cache"
		method = http.MethodPost
		key := map[string]string{"purge_url": "files", "purge_prefix": "prefixes", "purge_host": "hosts", "purge_tag": "tags"}[j.Operation]
		v := map[string]any{}
		if j.Operation == "purge_all" {
			v["purge_everything"] = true
		} else {
			targets := append([]string(nil), j.Targets...)
			if j.Operation == "purge_prefix" {
				for i, t := range targets {
					u, _ := url.Parse(t)
					targets[i] = u.Host + u.EscapedPath()
				}
			}
			v[key] = targets
		}
		body = jsonBody(v)
	}
	r := request(ctx, method, endpoint, body, "application/json")
	r.Header.Set("Authorization", "Bearer "+p.Secret)
	b, e := a.send(r)
	if e != nil {
		return Receipt{}, e
	}
	var out struct {
		Success bool `json:"success"`
		Result  struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"result"`
		Errors []struct {
			Code int `json:"code"`
		} `json:"errors"`
	}
	if json.Unmarshal(b, &out) != nil {
		return Receipt{}, &OperationError{"Cloudflare 返回格式异常", false, 0}
	}
	if !out.Success {
		code := "unknown"
		if len(out.Errors) > 0 {
			code = strconv.Itoa(out.Errors[0].Code)
		}
		status := 400
		if code == "1134" || code == "1015" {
			status = 429
		}
		return Receipt{}, remoteError(status, code, 0)
	}
	if j.Operation == "test" {
		if out.Result.Name == "" {
			return Receipt{}, invalidRemote()
		}
		for _, d := range p.Domains {
			if d != out.Result.Name && !strings.HasSuffix(d, "."+out.Result.Name) {
				// SaaS customer hostnames live outside the provider zone. Verify
				// them through that zone's API before accepting their cache scope.
				var hostnames []SaaSHostname
				if e := a.cfJSON(ctx, p, "GET", "/custom_hostnames?"+url.Values{"hostname": {d}, "per_page": {"50"}}.Encode(), nil, &hostnames); e != nil {
					return Receipt{}, e
				}
				found := false
				for _, h := range hostnames {
					if h.Hostname == d && resourcePattern.MatchString(h.ID) && h.Status == "active" && h.SSL.Status == "active" {
						found = true
						break
					}
				}
				if !found {
					return Receipt{}, &OperationError{"域名既不属于此 Cloudflare Zone，也不是其中已激活的 SaaS 主机名", false, 0}
				}
			}
		}
		return tested(), nil
	}
	if out.Result.ID == "" {
		return Receipt{}, &OperationError{"Cloudflare 未返回刷新回执，受理状态不明", false, 0}
	}
	return accepted(out.Result.ID), nil
}

func signTC3(r *http.Request, body []byte, p Profile, service string, now time.Time) {
	day := now.UTC().Format("2006-01-02")
	timestamp := strconv.FormatInt(now.Unix(), 10)
	scope := day + "/" + service + "/tc3_request"
	canonical := "POST\n/\n\ncontent-type:application/json; charset=utf-8\nhost:" + r.URL.Host + "\n\ncontent-type;host\n" + digest(body)
	toSign := "TC3-HMAC-SHA256\n" + timestamp + "\n" + scope + "\n" + digest([]byte(canonical))
	dateKey := mac(sha256.New, []byte("TC3"+p.Secret), day)
	serviceKey := mac(sha256.New, dateKey, service)
	key := mac(sha256.New, serviceKey, "tc3_request")
	r.Header.Set("Authorization", "TC3-HMAC-SHA256 Credential="+p.AccessKey+"/"+scope+", SignedHeaders=content-type;host, Signature="+hex.EncodeToString(mac(sha256.New, key, toSign)))
	r.Header.Set("X-TC-Timestamp", timestamp)
	if p.Token != "" {
		r.Header.Set("X-TC-Token", p.Token)
	}
}
func (a *ProviderClient) tencent(ctx context.Context, p Profile, j Job) (Receipt, error) {
	service, version, action := "cdn", "2018-06-06", ""
	v := map[string]any{}
	if p.Provider == "edgeone" {
		service, version = "teo", "2022-09-01"
		v["ZoneId"] = p.ResourceID
		switch j.Operation {
		case "test":
			action = "DescribeZones"
			v = map[string]any{"Filters": []any{map[string]any{"Name": "zone-id", "Values": []string{p.ResourceID}}}}
		case "warm":
			action = "CreatePrefetchTask"
			v["Targets"] = j.Targets
		default:
			action = "CreatePurgeTask"
			v["Type"] = j.Operation
			v["Method"] = "delete"
			if j.Operation != "purge_all" {
				targets := append([]string(nil), j.Targets...)
				if j.Operation == "purge_host" {
					for i, d := range targets {
						targets[i] = "https://" + d
					}
				}
				v["Targets"] = targets
			}
		}
	} else {
		switch j.Operation {
		case "test":
			action = "DescribeDomains"
			v["Filters"] = []any{map[string]any{"Name": "domain", "Value": []string{p.Domains[0]}, "Fuzzy": false}}
		case "warm":
			action = "PushUrlsCache"
			v["Urls"] = j.Targets
		case "purge_directory":
			action = "PurgePathCache"
			v["Paths"] = j.Targets
			v["FlushType"] = "flush"
		default:
			action = "PurgeUrlsCache"
			v["Urls"] = j.Targets
		}
	}
	body := jsonBody(v)
	r := request(ctx, http.MethodPost, "https://"+service+".tencentcloudapi.com/", body, "application/json; charset=utf-8")
	r.Header.Set("X-TC-Action", action)
	r.Header.Set("X-TC-Version", version)
	signTC3(r, body, p, service, a.now())
	b, e := a.send(r)
	if e != nil {
		return Receipt{}, e
	}
	var out struct {
		Response *struct {
			TaskId      string
			JobId       string
			RequestId   string
			Error       *struct{ Code string }
			TotalCount  *int64
			TotalNumber *int64
			FailedList  []json.RawMessage
		}
	}
	if json.Unmarshal(b, &out) != nil || out.Response == nil {
		return Receipt{}, &OperationError{"腾讯云返回格式异常", false, 0}
	}
	vv := out.Response
	if vv.Error != nil {
		return Receipt{}, remoteError(400, vv.Error.Code, 0)
	}
	if len(vv.FailedList) > 0 {
		return Receipt{}, &OperationError{fmt.Sprintf("EdgeOne 部分受理：%d 个目标失败，请在控制台核对后修正；不会自动重试整批", len(vv.FailedList)), false, 0}
	}
	if j.Operation == "test" {
		if vv.RequestId == "" || vv.TotalCount == nil && vv.TotalNumber == nil {
			return Receipt{}, &OperationError{"腾讯云资源查询返回格式异常", false, 0}
		}
		if vv.TotalCount != nil && *vv.TotalCount == 0 || vv.TotalNumber != nil && *vv.TotalNumber == 0 {
			return Receipt{}, &OperationError{"当前凭据未查询到所选资源", false, 0}
		}
		return tested(), nil
	}
	id := vv.TaskId
	if id == "" {
		id = vv.JobId
	}
	if id == "" {
		return Receipt{}, &OperationError{"腾讯云未返回任务 ID，受理状态不明", false, 0}
	}
	return accepted(id), nil
}

func percent(s string) string { return strings.ReplaceAll(url.QueryEscape(s), "+", "%20") }
func signAli(values url.Values, secret string) string {
	canonical := strings.ReplaceAll(values.Encode(), "+", "%20")
	return base64.StdEncoding.EncodeToString(mac(sha1.New, []byte(secret+"&"), "POST&%2F&"+percent(canonical)))
}
func (a *ProviderClient) aliyun(ctx context.Context, p Profile, j Job) (Receipt, error) {
	v := url.Values{"Format": {"JSON"}, "Version": {"2018-05-10"}, "AccessKeyId": {p.AccessKey}, "SignatureMethod": {"HMAC-SHA1"}, "SignatureVersion": {"1.0"}, "Timestamp": {a.now().UTC().Format("2006-01-02T15:04:05Z")}, "SignatureNonce": {randomID()}}
	if p.Token != "" {
		v.Set("SecurityToken", p.Token)
	}
	switch j.Operation {
	case "test":
		v.Set("Action", "DescribeCdnDomainDetail")
		v.Set("DomainName", p.Domains[0])
	case "warm":
		v.Set("Action", "PushObjectCache")
		v.Set("ObjectPath", strings.Join(j.Targets, "\n"))
	default:
		v.Set("Action", "RefreshObjectCaches")
		v.Set("ObjectPath", strings.Join(j.Targets, "\n"))
		v.Set("ObjectType", "File")
		if j.Operation == "purge_directory" {
			v.Set("ObjectType", "Directory")
			v.Set("Force", "true")
		}
	}
	v.Set("Signature", signAli(v, p.Secret))
	b, e := a.send(request(ctx, http.MethodPost, "https://cdn.aliyuncs.com/", []byte(v.Encode()), "application/x-www-form-urlencoded"))
	if e != nil {
		return Receipt{}, e
	}
	var out struct {
		Code          string
		RefreshTaskId string
		PushTaskId    string
		RequestId     string
	}
	if json.Unmarshal(b, &out) != nil {
		return Receipt{}, &OperationError{"阿里云返回格式异常", false, 0}
	}
	if out.Code != "" {
		return Receipt{}, remoteError(400, out.Code, 0)
	}
	if j.Operation == "test" {
		if out.RequestId == "" {
			return Receipt{}, errors.New("missing receipt")
		}
		return tested(), nil
	}
	id := out.RefreshTaskId
	if id == "" {
		id = out.PushTaskId
	}
	if id == "" {
		return Receipt{}, &OperationError{"阿里云未返回任务 ID，受理状态不明", false, 0}
	}
	return accepted(id), nil
}
func (a *ProviderClient) qiniu(ctx context.Context, p Profile, j Job) (Receipt, error) {
	endpoint := "/v2/tune/refresh"
	v := map[string]any{"urls": j.Targets}
	switch j.Operation {
	case "warm":
		endpoint = "/v2/tune/prefetch"
	case "purge_directory":
		v = map[string]any{"dirs": j.Targets}
	case "test":
		endpoint = "/v2/tune/log/list"
		v = map[string]any{"day": a.now().AddDate(0, 0, -1).Format("2006-01-02"), "domains": p.Domains[0]}
	}
	r := request(ctx, http.MethodPost, "https://fusion.qiniuapi.com"+endpoint, jsonBody(v), "application/json")
	// QBox v1 signs the path and query, followed by LF. JSON bodies are not
	// included (only application/x-www-form-urlencoded bodies are included).
	r.Header.Set("Authorization", "QBox "+p.AccessKey+":"+base64.URLEncoding.EncodeToString(mac(sha1.New, []byte(p.Secret), endpoint+"\n")))
	b, e := a.send(r)
	if e != nil {
		return Receipt{}, e
	}
	var out struct {
		Code        int      `json:"code"`
		RequestID   string   `json:"requestId"`
		InvalidURLs []string `json:"invalidUrls"`
		InvalidDirs []string `json:"invalidDirs"`
		URLSurplus  int      `json:"urlSurplusDay"`
		DirSurplus  int      `json:"dirSurplusDay"`
		Surplus     int      `json:"surplusDay"`
	}
	if json.Unmarshal(b, &out) != nil {
		return Receipt{}, &OperationError{"七牛云返回格式异常", false, 0}
	}
	if out.Code != 200 {
		return Receipt{}, remoteError(out.Code, strconv.Itoa(out.Code), 0)
	}
	if len(out.InvalidURLs)+len(out.InvalidDirs) > 0 {
		return Receipt{}, &OperationError{fmt.Sprintf("七牛云部分受理：%d 个无效 URL / 目录，请修正后重新提交；不会自动重试整批", len(out.InvalidURLs)+len(out.InvalidDirs)), false, 0}
	}
	if j.Operation == "test" {
		return tested(), nil
	}
	rct := accepted(out.RequestID)
	rct.Details = map[string]string{"url_remaining": strconv.Itoa(out.URLSurplus), "directory_remaining": strconv.Itoa(out.DirSurplus), "warm_remaining": strconv.Itoa(out.Surplus)}
	return rct, nil
}

type invalidationBatch struct {
	XMLName xml.Name `xml:"InvalidationBatch"`
	XMLNS   string   `xml:"xmlns,attr"`
	Paths   struct {
		Quantity int      `xml:"Quantity"`
		Items    []string `xml:"Items>Path"`
	} `xml:"Paths"`
	CallerReference string `xml:"CallerReference"`
}

func (a *ProviderClient) cloudfront(ctx context.Context, p Profile, j Job) (Receipt, error) {
	endpoint := "https://cloudfront.amazonaws.com/2020-05-31/distribution/" + p.ResourceID
	method := http.MethodGet
	var body []byte
	if j.Operation != "test" {
		method = http.MethodPost
		endpoint += "/invalidation"
		batch := invalidationBatch{XMLNS: "http://cloudfront.amazonaws.com/doc/2020-05-31/", CallerReference: "newpress-" + j.ID}
		for _, t := range j.Targets {
			u, _ := url.Parse(t)
			path := u.EscapedPath()
			if path == "" {
				path = "/"
			}
			if u.RawQuery != "" {
				path += "?" + u.RawQuery
			}
			if j.Operation == "purge_prefix" {
				path += "*"
			}
			batch.Paths.Items = append(batch.Paths.Items, path)
		}
		if j.Operation == "purge_all" {
			batch.Paths.Items = []string{"/*"}
		}
		batch.Paths.Quantity = len(batch.Paths.Items)
		body, _ = xml.Marshal(batch)
	}
	r := request(ctx, method, endpoint, body, "application/xml")
	if e := v4.NewSigner().SignHTTP(ctx, aws.Credentials{AccessKeyID: p.AccessKey, SecretAccessKey: p.Secret, SessionToken: p.Token}, r, digest(body), "cloudfront", "us-east-1", a.now()); e != nil {
		return Receipt{}, e
	}
	b, e := a.send(r)
	if e != nil {
		return Receipt{}, e
	}
	if j.Operation == "test" {
		var out struct {
			ID string `xml:"Id"`
		}
		if xml.Unmarshal(b, &out) != nil || out.ID != p.ResourceID {
			return Receipt{}, &OperationError{"CloudFront 分配验证失败", false, 0}
		}
		return tested(), nil
	}
	var out struct {
		ID     string `xml:"Id"`
		Status string `xml:"Status"`
	}
	if xml.Unmarshal(b, &out) != nil || out.ID == "" {
		return Receipt{}, &OperationError{"CloudFront 返回格式异常", false, 0}
	}
	return accepted(out.ID), nil
}
