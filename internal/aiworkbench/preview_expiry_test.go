package aiworkbench

import (
	"errors"
	"testing"
	"time"
)

func TestPreviewExpiresDuringAuthorityCheck(t *testing.T) {
	for _, at := range []int{1, 2} {
		t.Run(string(rune('0'+at)), func(t *testing.T) {
			var previews Previews
			now := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
			p, err := previews.Put(1, Preview{Context: "private snapshot"}, now.Add(-PreviewTTL+20*time.Millisecond))
			if err != nil {
				t.Fatal(err)
			}
			checks := 0
			got, err := previews.TakeChecked(1, p.Token, now, func() error {
				checks++
				if checks == at {
					time.Sleep(40 * time.Millisecond)
				}
				return nil
			})
			if !errors.Is(err, ErrPreview) || got.Context != "" {
				t.Fatal("expired snapshot returned after authority wait", err)
			}
			if _, err := previews.Take(1, p.Token, now); !errors.Is(err, ErrPreview) {
				t.Fatal("expired token retained")
			}
		})
	}
}
