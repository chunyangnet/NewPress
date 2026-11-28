package httpserver

import (
	"sort"
	"time"

	"newpress.local/newpress/internal/community"
)

func sortCircleTopics(topics []community.Topic, order string, manage bool) {
	if len(topics) < 2 {
		return
	}
	var activity map[int64]time.Time
	if order == "active" {
		activity = make(map[int64]time.Time, len(topics))
		for _, topic := range topics {
			latest := topic.CreatedAt
			for _, reply := range topic.Replies {
				if (!reply.Hidden || manage) && reply.CreatedAt.After(latest) {
					latest = reply.CreatedAt
				}
			}
			activity[topic.ID] = latest
		}
	}
	sort.Slice(topics, func(i, j int) bool {
		if topics[i].Pinned != topics[j].Pinned {
			return topics[i].Pinned
		}
		if activity != nil {
			left, right := activity[topics[i].ID], activity[topics[j].ID]
			if !left.Equal(right) {
				return left.After(right)
			}
		}
		return topics[i].ID > topics[j].ID
	})
}
