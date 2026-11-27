package cdn

import (
	"context"
	"slices"
	"strings"
	"time"
)

func recordSnapshot(records []DNSRecord, name string) []DNSRecord {
	result := []DNSRecord{}
	for _, r := range records {
		if r.Name == name {
			result = append(result, canonicalRecord(r))
		}
	}
	slices.SortFunc(result, func(a, b DNSRecord) int { return strings.Compare(a.ID, b.ID) })
	return result
}
func sameRecords(a, b []DNSRecord) bool {
	a = slices.Clone(a)
	b = slices.Clone(b)
	slices.SortFunc(a, func(x, y DNSRecord) int { return strings.Compare(x.ID, y.ID) })
	slices.SortFunc(b, func(x, y DNSRecord) int { return strings.Compare(x.ID, y.ID) })
	return slices.Equal(a, b)
}
func ownsRecord(st RoutingState, r Route, d DNSProfile, record DNSRecord) bool {
	return slices.ContainsFunc(st.Owned, func(o ownedRecord) bool {
		return o.RouteID == r.ID && o.DNSIdentity == dnsIdentity(d) && o.Record == record
	})
}
func routingReady(r Route, report RouteReport, preferred bool) error {
	if report.Hostname.ID == "" || report.Hostname.Status != "active" || report.Hostname.SSL.Status != "active" || report.Fallback.Status != "active" || cleanHost(report.Fallback.Origin) != r.FallbackOrigin {
		return routeError("主机名、证书和回源必须全部 active 且回源与方案一致，才能切流")
	}
	if report.Hostname.CustomOrigin != "" && cleanHost(report.Hostname.CustomOrigin) != r.FallbackOrigin {
		return routeError("自定义主机名的独立回源与当前方案不一致")
	}
	if !report.Delegated {
		return routeError("NS 委派未匹配，暂不能切流")
	}
	if !report.Normal.Healthy || preferred && !report.Preferred.Healthy {
		return routeError("常规线路或优选线路未通过 HTTPS 健康检查；请使用返回 2xx 的公开检查路径")
	}
	return nil
}
func buildRoutingPlan(task RoutingTask, r Route, d DNSProfile, cf Profile, report RouteReport, st RoutingState) (RoutingPlan, error) {
	now := time.Now().UTC()
	kind := strings.TrimPrefix(task.Action, "preview_")
	p := RoutingPlan{ID: randomID(), RouteID: r.ID, Kind: kind, Identity: routeIdentity(r, d, cf), Actor: task.Actor, Created: now, Expires: now.Add(10 * time.Minute), Status: "ready", Changes: []DNSChange{}, Snapshots: map[string][]DNSRecord{}, HostnameID: report.Hostname.ID}
	if kind == "setup" {
		p.FallbackBefore = report.Fallback.Origin
		p.FallbackAfter = r.FallbackOrigin
		if p.FallbackBefore != p.FallbackAfter {
			p.FallbackState = "pending"
		}
		p.CreateHostname = report.Hostname.ID == ""
		if p.CreateHostname {
			p.HostnameState = "pending"
		}
		return p, nil
	}
	if kind == "rollback" {
		old, ok := findRoutingPlan(st, task.PlanID)
		if !ok || old.Identity != p.Identity || old.RouteID != r.ID || old.Kind == "setup" {
			return p, ErrConflict
		}
		p.RollbackOf = old.ID
		for i := len(old.Changes) - 1; i >= 0; i-- {
			change := old.Changes[i]
			if change.State != "done" || change.Kind == "adopt" {
				continue
			}
			var inverse DNSChange
			switch change.Kind {
			case "create":
				inverse = DNSChange{Kind: "delete", Before: change.After, State: "pending"}
			case "update":
				inverse = DNSChange{Kind: "update", Before: change.After, After: change.Before, State: "pending"}
			case "delete":
				return p, routeError("回滚计划不支持再次反向回滚，请重新生成接入计划")
			}
			if inverse.Before == nil {
				return p, invalidRemote()
			}
			current := recordSnapshot(report.Records, inverse.Before.Name)
			if !slices.Contains(current, *inverse.Before) {
				return p, routeError("记录已被外部修改或删除，不能覆盖回滚：" + inverse.Before.Name)
			}
			p.Snapshots[inverse.Before.Name] = current
			p.Changes = append(p.Changes, inverse)
		}
		if len(p.Changes) == 0 {
			return p, routeError("没有可安全回滚且已确认完成的 DNS 步骤")
		}
		return p, nil
	}
	if r.TTL < report.DNS.MinTTL {
		return p, routeError("TTL 小于此 DNS 套餐的最小值")
	}
	var wanted []DNSRecord
	if kind == "validation" {
		if report.Hostname.ID == "" {
			return p, routeError("请先执行 SaaS 准备计划，再读取云端验证记录")
		}
		if len(report.Validation) == 0 {
			return p, routeError("Cloudflare 尚未返回 DNS 验证记录；稍后检查，或查看当前 HTTP 验证要求")
		}
		wanted = report.Validation
	} else {
		if len(r.PreferredLines) == 0 {
			return p, routeError("请先检查接入，从返回的可用线路中选择至少一条优选线路并保存")
		}
		if e := routingReady(r, report, kind != "fallback"); e != nil {
			return p, e
		}
		wanted = append(wanted, DNSRecord{Name: r.Hostname, Type: "CNAME", Value: r.Target, Line: report.DNS.DefaultLine, TTL: r.TTL, Status: "ENABLE"})
		for _, line := range r.PreferredLines {
			if !slices.ContainsFunc(report.DNS.Lines, func(l DNSLine) bool { return l.ID == line }) {
				return p, routeError("当前 DNS 套餐不支持所选线路：" + line)
			}
			target := r.PreferredTarget
			if kind == "fallback" {
				target = r.Target
			}
			wanted = append(wanted, DNSRecord{Name: r.Hostname, Type: "CNAME", Value: target, Line: line, TTL: r.TTL, Status: "ENABLE"})
		}
	}
	for _, desired := range wanted {
		if desired.Line != report.DNS.DefaultLine && !slices.ContainsFunc(report.DNS.Lines, func(l DNSLine) bool { return l.ID == desired.Line }) {
			return p, invalidRemote()
		}
		current := recordSnapshot(report.Records, desired.Name)
		p.Snapshots[desired.Name] = current
		var matches []DNSRecord
		for _, v := range current {
			// Provider apex NS/SOA records are implicit delegation, never edited.
			if v.Name == d.Zone && (v.Type == "NS" || v.Type == "SOA") {
				continue
			}
			if kind == "validation" {
				if v.Type == "CNAME" || desired.Type == "CNAME" {
					if v.Type != desired.Type || v.Value != desired.Value {
						return p, routeError("验证记录存在 CNAME/TXT 冲突，请先在 DNS 控制台核对：" + v.Name)
					}
				}
				if v.Line == desired.Line && v.Type == desired.Type && v.Value == desired.Value {
					matches = append(matches, v)
				}
			} else if v.Line == desired.Line {
				if v.Type != "CNAME" {
					return p, routeError("切流名称存在非 CNAME 记录，插件不会删除 A/AAAA/TXT/MX 等解析：" + v.Name)
				}
				matches = append(matches, v)
			}
		}
		if len(matches) > 1 {
			return p, routeError("同一线路存在多条记录，不能安全更新")
		}
		if len(matches) == 0 {
			p.Changes = append(p.Changes, DNSChange{Kind: "create", After: &desired, State: "pending"})
			continue
		}
		before := matches[0]
		desired.ID = before.ID
		if before.Status != "ENABLE" || before.Locked || before.Weight != 0 {
			return p, routeError("记录已暂停、锁定或配置负载权重，不能由此方案接管")
		}
		if kind == "validation" {
			continue
		} // Keep all other certificate TXT tokens intact.
		owned := ownsRecord(st, r, d, before)
		if before == desired {
			if !owned && r.ManageExisting {
				p.Changes = append(p.Changes, DNSChange{Kind: "adopt", Before: &before, After: &desired, State: "pending"})
			}
			continue
		}
		if !owned && !r.ManageExisting {
			return p, routeError("存在未由此方案管理的 CNAME；如需更改，请启用“接管已有 CNAME”后重新预览")
		}
		p.Changes = append(p.Changes, DNSChange{Kind: "update", Before: &before, After: &desired, State: "pending"})
	}
	if len(p.Changes) > 32 {
		return p, routeError("单次计划最多 32 个 DNS 变更")
	}
	return p, nil
}

func (s *Service) applyRoutingPlan(ctx context.Context, g uint64, task RoutingTask, r Route, d DNSProfile, cf Profile, report RouteReport, p RoutingPlan) (resultErr error) {
	if p.Status != "ready" || time.Now().After(p.Expires) || p.Identity != routeIdentity(r, d, cf) || p.HostnameID != report.Hostname.ID {
		return ErrConflict
	}
	if p.Kind == "route" || p.Kind == "restore" || p.Kind == "fallback" {
		if e := routingReady(r, report, p.Kind != "fallback"); e != nil {
			return e
		}
	}
	if p.Kind == "validation" {
		for _, change := range p.Changes {
			if change.After == nil {
				return invalidRemote()
			}
			found := slices.ContainsFunc(report.Validation, func(v DNSRecord) bool { v.ID = change.After.ID; return v == *change.After })
			if !found {
				return routeError("验证令牌已变化，请重新生成验证计划")
			}
		}
	}
	if p.Kind == "setup" && report.Fallback.Origin != p.FallbackBefore {
		return ErrConflict
	}
	// Compare complete RRsets at every touched name, not just a chosen record ID.
	expected := map[string][]DNSRecord{}
	for name, before := range p.Snapshots {
		current, e := s.api.dnsRecords(ctx, d, name)
		if e != nil {
			return e
		}
		if !sameRecords(before, current) {
			return routeError("DNS 已在预览后变化，请重新生成计划：" + name)
		}
		expected[name] = slices.Clone(current)
	}
	p.Status = "applying"
	save := func(enabled bool) error {
		return s.with(enabled, g, true, func(st *diskState) error { return putPlan(&st.Routing, p) })
	}
	if e := save(true); e != nil {
		return e
	}
	defer func() {
		if resultErr != nil {
			p.Status = "partial"
			p.Error = PublicError(resultErr)
			if p.FallbackState == "attempting" || p.HostnameState == "attempting" || slices.ContainsFunc(p.Changes, func(c DNSChange) bool { return c.State == "attempting" || c.State == "unknown" }) {
				p.Status = "unknown"
			}
		}
		if e := save(false); e != nil && resultErr == nil {
			resultErr = e
		}
	}()
	guard := func() error {
		if e := ctx.Err(); e != nil {
			return e
		}
		return s.with(true, g, false, func(st *diskState) error {
			current, ok := findRoutingPlan(st.Routing, p.ID)
			if !ok || current.Status != "applying" {
				return ErrConflict
			}
			return nil
		})
	}
	if p.Kind == "setup" {
		if p.FallbackState == "pending" {
			if e := guard(); e != nil {
				return e
			}
			p.FallbackState = "attempting"
			if e := save(true); e != nil {
				return e
			}
			var f FallbackOrigin
			if e := s.api.cfJSON(ctx, cf, "PUT", "/custom_hostnames/fallback_origin", map[string]any{"origin": p.FallbackAfter}, &f); e != nil {
				return e
			}
			if cleanHost(f.Origin) != p.FallbackAfter {
				return invalidRemote()
			}
			p.FallbackState = "done"
			if e := save(true); e != nil {
				return e
			}
		}
		if p.CreateHostname {
			if e := guard(); e != nil {
				return e
			}
			p.HostnameState = "attempting"
			if e := save(true); e != nil {
				return e
			}
			var hostname SaaSHostname
			body := map[string]any{"hostname": r.Hostname, "ssl": map[string]any{"method": "txt", "type": "dv", "settings": map[string]any{"min_tls_version": "1.2"}}}
			if e := s.api.cfJSON(ctx, cf, "POST", "/custom_hostnames", body, &hostname); e != nil {
				return e
			}
			if hostname.Hostname != r.Hostname || !resourcePattern.MatchString(hostname.ID) {
				return invalidRemote()
			}
			p.HostnameID = hostname.ID
			p.HostnameState = "done"
			if e := save(true); e != nil {
				return e
			}
		}
		p.Status = "applied"
		return nil
	}
	for index, change := range p.Changes {
		if e := guard(); e != nil {
			return e
		}
		record := change.After
		if record == nil {
			record = change.Before
		}
		if record == nil {
			return invalidRemote()
		}
		name := record.Name
		current, e := s.api.dnsRecords(ctx, d, name)
		if e != nil {
			return e
		}
		if !sameRecords(expected[name], current) {
			return routeError("执行期间 DNS 变化，已停止后续步骤：" + name)
		}
		p.Changes[index].State = "attempting"
		if e = save(true); e != nil {
			return e
		}
		after, e := s.api.writeDNS(ctx, d, change)
		if e != nil {
			p.Changes[index].State = "unknown"
			return e
		}
		if change.Kind == "update" && (after == nil || after.ID != change.Before.ID) {
			return invalidRemote()
		}
		p.Changes[index].After = after
		p.Changes[index].State = "done"
		if change.Before != nil {
			current = slices.DeleteFunc(current, func(v DNSRecord) bool { return v.ID == change.Before.ID })
		}
		if after != nil {
			current = append(current, *after)
		}
		expected[name] = current
		e = s.with(true, g, true, func(st *diskState) error {
			if e := putPlan(&st.Routing, p); e != nil {
				return e
			}
			if change.Before != nil {
				st.Routing.Owned = slices.DeleteFunc(st.Routing.Owned, func(o ownedRecord) bool { return o.DNSIdentity == dnsIdentity(d) && o.Record.ID == change.Before.ID })
			}
			if after != nil {
				if len(st.Routing.Owned) >= 500 {
					return routeError("已管理记录超过 500 条限制")
				}
				st.Routing.Owned = append(st.Routing.Owned, ownedRecord{dnsIdentity(d), r.ID, *after})
			}
			return nil
		})
		if e != nil {
			return e
		}
		if e = s.awaitDNS(ctx, d, name, expected[name]); e != nil {
			return e
		}
	}
	p.Status = "applied"
	return s.with(true, g, true, func(st *diskState) error {
		m := st.Routing.Monitors[r.ID]
		m.LastSwitch = time.Now().UTC()
		m.Failures = 0
		m.Successes = 0
		st.Routing.Monitors[r.ID] = m
		if rr, ok := st.Routing.Reports[r.ID]; ok {
			for name, records := range expected {
				rr.Records = slices.DeleteFunc(rr.Records, func(v DNSRecord) bool { return v.Name == name })
				rr.Records = append(rr.Records, records...)
			}
			rr.Mode = routeMode(r, rr.Records, rr.DNS.DefaultLine)
			st.Routing.Reports[r.ID] = rr
		}
		if p.RollbackOf != "" {
			for i, old := range st.Routing.Plans {
				if old.ID == p.RollbackOf && old.Status == "applied" {
					st.Routing.Plans[i].Status = "rolled_back"
				}
			}
		}
		return nil
	})
}

// DNSPod documents a short indexing delay (retry after 30 seconds). Retry reads
// only: never issue the create/update again because its response was delayed.
func (s *Service) awaitDNS(ctx context.Context, d DNSProfile, name string, expected []DNSRecord) error {
	deadline := time.Now().Add(40 * time.Second)
	delay := time.Second
	for {
		verified, e := s.api.dnsRecords(ctx, d, name)
		if e != nil {
			return e
		}
		if sameRecords(expected, verified) {
			return nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return routeError("写入回执已保存，40 秒内读取结果仍未一致或记录被修改；已停止后续步骤")
		}
		timer := time.NewTimer(min(delay, remaining))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		delay = min(delay*2, 10*time.Second)
	}
}

func (s *Service) monitorRoute(ctx context.Context, g uint64, task RoutingTask, r Route, d DNSProfile, cf Profile, report RouteReport, snapshot RoutingState) error {
	m := snapshot.Monitors[r.ID]
	if report.Normal.Checked.IsZero() || report.Preferred.Checked.IsZero() {
		m.Failures = 0
		m.Successes = 0
	} else if report.Preferred.Healthy && report.Normal.Healthy {
		m.Successes++
		m.Failures = 0
	} else if report.Normal.Healthy && !report.Preferred.Healthy {
		m.Failures++
		m.Successes = 0
	} else {
		m.Failures = 0
		m.Successes = 0
	}
	action := ""
	if r.AutoFailover && report.Mode == "preferred" && m.Failures >= r.FailureThreshold {
		action = "preview_fallback"
	}
	if r.AutoRecover && report.Mode == "fallback" && m.Successes >= r.RecoveryThreshold {
		action = "preview_restore"
	}
	if time.Since(m.LastSwitch) < time.Duration(r.Cooldown)*time.Second {
		action = ""
	}
	if e := s.with(true, g, true, func(st *diskState) error { st.Routing.Monitors[r.ID] = m; return nil }); e != nil {
		return e
	}
	if action == "" {
		return nil
	}
	for _, line := range append([]string{report.DNS.DefaultLine}, r.PreferredLines...) {
		matches := []DNSRecord{}
		for _, record := range report.Records {
			if record.Name == r.Hostname && record.Line == line {
				matches = append(matches, record)
			}
		}
		if len(matches) != 1 || matches[0].Type != "CNAME" || !ownsRecord(snapshot, r, d, matches[0]) {
			return routeError("自动切换已停止：存在外部修改或未接管的 CNAME；重新预览接入后再启用")
		}
	}
	task.Action = action
	plan, e := buildRoutingPlan(task, r, d, cf, report, snapshot)
	if e != nil {
		return e
	}
	if e = s.with(true, g, true, func(st *diskState) error {
		m.LastSwitch = time.Now().UTC()
		m.Failures = 0
		m.Successes = 0
		st.Routing.Monitors[r.ID] = m
		for i, t := range st.Routing.Tasks {
			if t.ID == task.ID {
				st.Routing.Tasks[i].PlanID = plan.ID
			}
		}
		return putPlan(&st.Routing, plan)
	}); e != nil {
		return e
	}
	return s.applyRoutingPlan(ctx, g, task, r, d, cf, report, plan)
}
