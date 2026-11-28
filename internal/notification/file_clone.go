package notification

import (
	"maps"
	"slices"
	"time"
)

// Each candidate owns every mutable map, slice and time pointer. Failed
// mutations must not alter live state, including inbox timestamps and leases.
func cloneDocument(d document) document {
	result := d
	result.Preferences = maps.Clone(d.Preferences)
	result.Receipts = maps.Clone(d.Receipts)
	result.Notices = slices.Clone(d.Notices)
	for i := range result.Notices {
		n := &result.Notices[i]
		n.Delivery = cloneDelivery(n.Delivery)
		n.ReadAt = cloneNotificationTime(n.ReadAt)
		n.ClearedAt = cloneNotificationTime(n.ClearedAt)
	}
	result.Jobs = maps.Clone(d.Jobs)
	for key, job := range result.Jobs {
		job.Delivery = cloneDelivery(job.Delivery)
		job.LeaseUntil = cloneNotificationTime(job.LeaseUntil)
		result.Jobs[key] = job
	}
	return result
}

func cloneNotificationTime(at *time.Time) *time.Time {
	if at == nil {
		return nil
	}
	copy := *at
	return &copy
}
