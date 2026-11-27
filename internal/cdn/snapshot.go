package cdn

import (
	"maps"
	"slices"
)

// Jobs cross service boundaries by value, including their mutable children.
// Caller edits must not change queued work or later become durable indirectly.
func cloneJob(j Job) Job {
	j.Targets = slices.Clone(j.Targets)
	if j.Receipt != nil {
		v := *j.Receipt
		v.Details = maps.Clone(v.Details)
		j.Receipt = &v
	}
	if j.BenchmarkRequest != nil {
		v := *j.BenchmarkRequest
		v.Candidates = slices.Clone(v.Candidates)
		j.BenchmarkRequest = &v
	}
	if j.Benchmark != nil {
		v := *j.Benchmark
		v.Results = slices.Clone(v.Results)
		j.Benchmark = &v
	}
	if j.Diagnostic != nil {
		v := *j.Diagnostic
		v.Headers = maps.Clone(v.Headers)
		v.Findings = slices.Clone(v.Findings)
		j.Diagnostic = &v
	}
	return j
}

// Working plans and reports keep changing between checkpoints. The committed
// state must own their nested values so failed saves cannot change its receipt.
func cloneRoutingPlan(p RoutingPlan) RoutingPlan {
	p.Changes = slices.Clone(p.Changes)
	for i := range p.Changes {
		if p.Changes[i].Before != nil {
			v := *p.Changes[i].Before
			p.Changes[i].Before = &v
		}
		if p.Changes[i].After != nil {
			v := *p.Changes[i].After
			p.Changes[i].After = &v
		}
	}
	p.Snapshots = maps.Clone(p.Snapshots)
	for name, records := range p.Snapshots {
		p.Snapshots[name] = slices.Clone(records)
	}
	return p
}

func cloneRouteReport(r RouteReport) RouteReport {
	r.Hostname.SSL.Validation = slices.Clone(r.Hostname.SSL.Validation)
	r.Hostname.SSL.Delegation = slices.Clone(r.Hostname.SSL.Delegation)
	r.DNS.Lines = slices.Clone(r.DNS.Lines)
	r.DNS.Nameservers = slices.Clone(r.DNS.Nameservers)
	r.ActualNS = slices.Clone(r.ActualNS)
	r.Records = slices.Clone(r.Records)
	r.Validation = slices.Clone(r.Validation)
	r.Preferred.Colos = slices.Clone(r.Preferred.Colos)
	r.Normal.Colos = slices.Clone(r.Normal.Colos)
	r.Warnings = slices.Clone(r.Warnings)
	return r
}
