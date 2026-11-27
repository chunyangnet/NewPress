package notification

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/activity"
	"newpress.local/newpress/internal/content"
)

func TestWeeklyDigestIgnoresPinningWhenSelectingRecentArticles(t *testing.T) {
	for _, oldPin := range []bool{false, true} {
		t.Run(fmt.Sprintf("old-pin-%v", oldPin), func(t *testing.T) {
			s, posts, _, _ := notificationFixture(t)
			end := WeekEnd(time.Now(), "Asia/Shanghai")
			start := end.AddDate(0, 0, -7)
			pinned := true
			if oldPin {
				if _, err := posts.Create(t.Context(), content.PostInput{Title: "过期置顶", Slug: "old-pinned", Status: content.StatusPublish, PublishedAt: start.Add(-time.Hour), Pinned: &pinned}); err != nil {
					t.Fatal(err)
				}
			}
			for i := range 12 {
				in := content.PostInput{Title: fmt.Sprintf("本周文章%02d", i), Slug: fmt.Sprintf("weekly-%02d", i), Status: content.StatusPublish, PublishedAt: start.Add(time.Duration(i) * time.Hour)}
				if i == 0 {
					in.Pinned = &pinned
				}
				if _, err := posts.Create(t.Context(), in); err != nil {
					t.Fatal(err)
				}
			}
			job := Job{Delivery: Delivery{Kind: Weekly, Event: activity.Event{At: start, Due: end}}}
			prefs := Defaults(2)
			prefs.WeeklySince = start
			body, ok, err := s.weeklyBody(t.Context(), job, prefs, s.Settings())
			if err != nil || !ok {
				t.Fatalf("weekly digest omitted recent articles: ok=%v err=%v body=%q", ok, err, body)
			}
			if strings.Contains(body, "过期置顶") || strings.Contains(body, "本周文章00") || strings.Contains(body, "本周文章01") {
				t.Fatalf("older pinned articles displaced the latest ten: %s", body)
			}
			previous := -1
			for i := 11; i >= 2; i-- {
				index := strings.Index(body, fmt.Sprintf("本周文章%02d", i))
				if index < 0 || index <= previous {
					t.Fatalf("article %d missing or out of chronological order: %s", i, body)
				}
				previous = index
			}
		})
	}
}
