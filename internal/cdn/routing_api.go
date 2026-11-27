package cdn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

func invalidRemote() error {
	return &OperationError{"云端返回不完整或不支持的结构，已停止操作", false, 0}
}
func routeError(message string) error { return &OperationError{message, false, 0} }

func (a *ProviderClient) cfJSON(ctx context.Context, p Profile, method, path string, body any, out any) error {
	var data []byte
	if body != nil {
		data = jsonBody(body)
	}
	r := request(ctx, method, "https://api.cloudflare.com/client/v4/zones/"+url.PathEscape(p.ResourceID)+path, data, "application/json")
	r.Header.Set("Authorization", "Bearer "+p.Secret)
	b, e := a.send(r)
	if e != nil {
		return e
	}
	var envelope struct {
		Success bool            `json:"success"`
		Result  json.RawMessage `json:"result"`
		Errors  []struct {
			Code int `json:"code"`
		} `json:"errors"`
	}
	if json.Unmarshal(b, &envelope) != nil {
		return invalidRemote()
	}
	if !envelope.Success {
		code := "unknown"
		if len(envelope.Errors) > 0 {
			code = strconv.Itoa(envelope.Errors[0].Code)
		}
		return remoteError(400, code, 0)
	}
	// An unconfigured fallback is a successful response with an optional result.
	if method == "GET" && path == "/custom_hostnames/fallback_origin" && (len(envelope.Result) == 0 || string(envelope.Result) == "null") {
		return nil
	}
	if out != nil && (len(envelope.Result) == 0 || string(envelope.Result) == "null" || json.Unmarshal(envelope.Result, out) != nil) {
		return invalidRemote()
	}
	return nil
}
func (a *ProviderClient) saasState(ctx context.Context, p Profile, r Route) (string, FallbackOrigin, SaaSHostname, error) {
	var zone struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	}
	var f FallbackOrigin
	var h SaaSHostname
	if e := a.cfJSON(ctx, p, "GET", "", nil, &zone); e != nil {
		return "", f, h, e
	}
	if zone.Status != "active" || !validHost(zone.Name) || !inZone(r.FallbackOrigin, zone.Name) || !inZone(r.Target, zone.Name) || r.Hostname == zone.Name {
		return "", f, h, routeError("SaaS Zone 必须有效，回源及常规 CNAME 目标必须属于此 Zone；访问域名不能是 Zone 本身")
	}
	if e := a.cfJSON(ctx, p, "GET", "/custom_hostnames/fallback_origin", nil, &f); e != nil {
		return "", f, h, e
	}
	var list []SaaSHostname
	if e := a.cfJSON(ctx, p, "GET", "/custom_hostnames?"+url.Values{"hostname": {r.Hostname}, "per_page": {"50"}}.Encode(), nil, &list); e != nil {
		return "", f, h, e
	}
	for _, v := range list {
		if cleanHost(v.Hostname) == r.Hostname {
			if h.ID != "" || !resourcePattern.MatchString(v.ID) {
				return "", f, h, invalidRemote()
			}
			h = v
		}
	}
	return zone.Name, f, h, nil
}
func (a *ProviderClient) validateProxied(ctx context.Context, p Profile, r Route) error {
	for _, name := range slices.Compact([]string{r.FallbackOrigin, r.Target}) {
		var records []struct {
			Name    string `json:"name"`
			Type    string `json:"type"`
			Content string `json:"content"`
			Proxied bool   `json:"proxied"`
		}
		if e := a.cfJSON(ctx, p, "GET", "/dns_records?"+url.Values{"name": {name}, "per_page": {"100"}}.Encode(), nil, &records); e != nil {
			return e
		}
		found := false
		for _, d := range records {
			if cleanHost(d.Name) == name && d.Proxied && slices.Contains([]string{"A", "AAAA", "CNAME"}, d.Type) {
				if cleanHost(d.Content) == r.Hostname || cleanHost(d.Content) == r.PreferredTarget {
					return routeError("回源记录指向访问域名或优选域名，会产生回环")
				}
				found = true
			}
		}
		if !found {
			return routeError("请先在 Cloudflare 添加已代理的回源/接入记录：" + name)
		}
	}
	return nil
}

func (a *ProviderClient) dnsJSON(ctx context.Context, p DNSProfile, action string, params map[string]any, out any) error {
	var b []byte
	var e error
	if p.Provider == "aliyun" {
		v := url.Values{"Format": {"JSON"}, "Version": {"2015-01-09"}, "AccessKeyId": {p.AccessKey}, "SignatureMethod": {"HMAC-SHA1"}, "SignatureVersion": {"1.0"}, "Timestamp": {a.now().UTC().Format("2006-01-02T15:04:05Z")}, "SignatureNonce": {randomID()}, "Action": {action}}
		for k, val := range params {
			v.Set(k, fmt.Sprint(val))
		}
		if p.Token != "" {
			v.Set("SecurityToken", p.Token)
		}
		v.Set("Signature", signAli(v, p.Secret))
		b, e = a.send(request(ctx, "POST", "https://alidns.aliyuncs.com/", []byte(v.Encode()), "application/x-www-form-urlencoded"))
		if e != nil {
			return e
		}
		var status struct {
			Code      string
			RequestId string
		}
		if json.Unmarshal(b, &status) != nil {
			return invalidRemote()
		}
		if status.Code != "" {
			return remoteError(400, status.Code, 0)
		}
		if status.RequestId == "" {
			return invalidRemote()
		}
	} else if p.Provider == "dnspod" {
		body := jsonBody(params)
		req := request(ctx, "POST", "https://dnspod.tencentcloudapi.com/", body, "application/json; charset=utf-8")
		req.Header.Set("X-TC-Action", action)
		req.Header.Set("X-TC-Version", "2021-03-23")
		signTC3(req, body, Profile{AccessKey: p.AccessKey, Secret: p.Secret, Token: p.Token}, "dnspod", a.now())
		b, e = a.send(req)
		if e != nil {
			return e
		}
		var envelope struct{ Response json.RawMessage }
		if json.Unmarshal(b, &envelope) != nil || len(envelope.Response) == 0 {
			return invalidRemote()
		}
		b = envelope.Response
		var status struct {
			Error     *struct{ Code string }
			RequestId string
		}
		if json.Unmarshal(b, &status) != nil {
			return invalidRemote()
		}
		if status.Error != nil {
			return remoteError(400, status.Error.Code, 0)
		}
		if status.RequestId == "" {
			return invalidRemote()
		}
	} else {
		return errors.New("unknown DNS provider")
	}
	if out != nil && json.Unmarshal(b, out) != nil {
		return invalidRemote()
	}
	return nil
}
func (a *ProviderClient) dnsZone(ctx context.Context, p DNSProfile) (DNSZone, error) {
	z := DNSZone{Name: p.Zone, DefaultLine: defaultDNSLine(p), Lines: []DNSLine{}}
	if p.Provider == "aliyun" {
		var info struct {
			DomainName string
			MinTtl     int
			DnsServers struct{ DnsServer []string }
			SlaveDns   bool
		}
		if e := a.dnsJSON(ctx, p, "DescribeDomainInfo", map[string]any{"DomainName": p.Zone}, &info); e != nil {
			return z, e
		}
		if cleanHost(info.DomainName) != p.Zone || info.SlaveDns {
			return z, routeError("DNS 区域不匹配或为辅助区域")
		}
		z.MinTTL = info.MinTtl
		z.Nameservers = info.DnsServers.DnsServer
		var lines struct {
			RecordLines struct {
				RecordLine []struct {
					LineCode        string
					LineName        string
					LineDisplayName string
				}
			}
		}
		if e := a.dnsJSON(ctx, p, "DescribeSupportLines", map[string]any{"DomainName": p.Zone, "Lang": "zh"}, &lines); e != nil {
			return z, e
		}
		for _, l := range lines.RecordLines.RecordLine {
			name := l.LineDisplayName
			if name == "" {
				name = l.LineName
			}
			z.Lines = append(z.Lines, DNSLine{l.LineCode, name})
		}
	} else {
		var info struct {
			DomainInfo struct {
				Domain       string
				Grade        string
				Status       string
				TTL          int
				DnspodNsList []string
				SlaveDNS     string
			}
		}
		if e := a.dnsJSON(ctx, p, "DescribeDomain", map[string]any{"Domain": p.Zone}, &info); e != nil {
			return z, e
		}
		d := info.DomainInfo
		if cleanHost(d.Domain) != p.Zone || d.Grade == "" || d.Status != "ENABLE" || strings.EqualFold(d.SlaveDNS, "yes") {
			return z, routeError("DNSPod 区域未启用、为辅助区域或返回区域不匹配")
		}
		// DNSPod DomainInfo.TTL is the default TTL, not a plan minimum.
		// Let its record API validate the selected TTL for the current plan.
		z.MinTTL = 0
		z.Nameservers = d.DnspodNsList
		var lines struct {
			LineList []struct {
				Name   string
				LineId string
			}
			LineGroupList []struct {
				Name   string
				LineId string
			}
		}
		if e := a.dnsJSON(ctx, p, "DescribeRecordLineList", map[string]any{"Domain": p.Zone, "DomainGrade": d.Grade}, &lines); e != nil {
			return z, e
		}
		for _, l := range lines.LineList {
			z.Lines = append(z.Lines, DNSLine{l.LineId, l.Name})
		}
		for _, l := range lines.LineGroupList {
			if !slices.ContainsFunc(z.Lines, func(v DNSLine) bool { return v.ID == l.LineId }) {
				z.Lines = append(z.Lines, DNSLine{l.LineId, l.Name})
			}
		}
	}
	if len(z.Lines) == 0 || len(z.Lines) > 2048 || len(z.Nameservers) == 0 || len(z.Nameservers) > 32 {
		return z, invalidRemote()
	}
	for i, ns := range z.Nameservers {
		z.Nameservers[i] = cleanHost(ns)
		if !validHost(z.Nameservers[i]) {
			return z, invalidRemote()
		}
	}
	if !slices.ContainsFunc(z.Lines, func(l DNSLine) bool { return l.ID == z.DefaultLine }) {
		return z, invalidRemote()
	}
	return z, nil
}
func canonicalRecord(r DNSRecord) DNSRecord {
	r.Name = cleanHost(r.Name)
	r.Type = strings.ToUpper(r.Type)
	r.Status = strings.ToUpper(r.Status)
	if r.Type == "CNAME" || r.Type == "NS" {
		r.Value = cleanHost(r.Value)
	}
	return r
}
func (a *ProviderClient) dnsRecords(ctx context.Context, p DNSProfile, name string) ([]DNSRecord, error) {
	if !inZone(name, p.Zone) {
		return nil, routeError("记录不属于所选 DNS 区域")
	}
	result := []DNSRecord{}
	seen := map[string]bool{}
	for page := 0; page < 5; page++ {
		var records []DNSRecord
		var total *int
		if p.Provider == "aliyun" {
			var out struct {
				TotalCount    *int
				DomainRecords struct {
					Record []struct {
						RecordId   string
						RR         string
						DomainName string
						Type       string
						Value      string
						Line       string
						TTL        int
						Status     string
						Weight     int
						Locked     bool
					}
				}
			}
			if e := a.dnsJSON(ctx, p, "DescribeDomainRecords", map[string]any{"DomainName": p.Zone, "RRKeyWord": relativeName(name, p.Zone), "SearchMode": "COMBINATION", "PageNumber": page + 1, "PageSize": 200}, &out); e != nil {
				return nil, e
			}
			total = out.TotalCount
			for _, v := range out.DomainRecords.Record {
				if v.DomainName != p.Zone {
					return nil, invalidRemote()
				}
				records = append(records, DNSRecord{ID: v.RecordId, Name: fullName(v.RR, p.Zone), Type: v.Type, Value: v.Value, Line: v.Line, TTL: v.TTL, Status: v.Status, Weight: v.Weight, Locked: v.Locked})
			}
		} else {
			var out struct {
				RecordCountInfo struct{ TotalCount *int }
				RecordList      []struct {
					RecordId uint64
					Name     string
					Type     string
					Value    string
					LineId   string
					TTL      int
					Status   string
					Weight   int
				}
			}
			if e := a.dnsJSON(ctx, p, "DescribeRecordList", map[string]any{"Domain": p.Zone, "SubDomain": relativeName(name, p.Zone), "Offset": page * 200, "Limit": 200, "ErrorOnEmpty": "no"}, &out); e != nil {
				return nil, e
			}
			total = out.RecordCountInfo.TotalCount
			for _, v := range out.RecordList {
				records = append(records, DNSRecord{ID: strconv.FormatUint(v.RecordId, 10), Name: fullName(v.Name, p.Zone), Type: v.Type, Value: v.Value, Line: v.LineId, TTL: v.TTL, Status: v.Status, Weight: v.Weight})
			}
		}
		if total == nil || *total < 0 || *total > 1000 || len(records) > 200 {
			return nil, routeError("DNS 返回缺少总数或记录超出 1000 条限制，无法安全生成计划")
		}
		for _, v := range records {
			v = canonicalRecord(v)
			if v.ID == "" || v.ID == "0" || seen[v.ID] || v.Name != name || len(v.Value) > 8192 {
				return nil, invalidRemote()
			}
			seen[v.ID] = true
			result = append(result, v)
		}
		if len(result) == *total {
			return result, nil
		}
		if len(records) == 0 || len(result) > *total {
			return nil, invalidRemote()
		}
	}
	return nil, routeError("DNS 分页不完整，已停止操作")
}
func (a *ProviderClient) writeDNS(ctx context.Context, p DNSProfile, change DNSChange) (*DNSRecord, error) {
	r := change.After
	if change.Kind == "delete" {
		r = change.Before
	}
	if r == nil || !inZone(r.Name, p.Zone) {
		return nil, invalidRemote()
	}
	if change.Kind == "adopt" {
		copy := *r
		return &copy, nil
	}
	var id string
	if p.Provider == "aliyun" {
		action := "AddDomainRecord"
		params := map[string]any{"DomainName": p.Zone, "RR": relativeName(r.Name, p.Zone), "Type": r.Type, "Value": r.Value, "TTL": r.TTL, "Line": r.Line}
		if change.Kind == "update" {
			action = "UpdateDomainRecord"
			delete(params, "DomainName")
			params["RecordId"] = change.Before.ID
		}
		if change.Kind == "delete" {
			action = "DeleteDomainRecord"
			params = map[string]any{"RecordId": r.ID}
		}
		var out struct{ RecordId string }
		if e := a.dnsJSON(ctx, p, action, params, &out); e != nil {
			return nil, e
		}
		id = out.RecordId
	} else {
		action := "CreateRecord"
		params := map[string]any{"Domain": p.Zone, "SubDomain": relativeName(r.Name, p.Zone), "RecordType": r.Type, "Value": r.Value, "TTL": r.TTL, "RecordLine": r.Line, "RecordLineId": r.Line}
		if change.Kind == "update" || change.Kind == "delete" {
			number, e := strconv.ParseUint(change.Before.ID, 10, 64)
			if e != nil || number == 0 {
				return nil, invalidRemote()
			}
			params["RecordId"] = number
			action = "ModifyRecord"
			if change.Kind == "delete" {
				action = "DeleteRecord"
				params = map[string]any{"Domain": p.Zone, "RecordId": number}
			}
		}
		var out struct{ RecordId uint64 }
		if e := a.dnsJSON(ctx, p, action, params, &out); e != nil {
			return nil, e
		}
		id = strconv.FormatUint(out.RecordId, 10)
		if change.Kind == "delete" {
			id = r.ID
		}
	}
	if id == "" || id == "0" {
		return nil, routeError("DNS 写入未返回记录 ID；结果未知，请检查云端记录")
	}
	if change.Kind == "delete" {
		return nil, nil
	}
	result := *r
	result.ID = id
	return &result, nil
}
