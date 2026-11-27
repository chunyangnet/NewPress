package database

import (
	"fmt"
	"testing"
	"time"

	"newpress.local/newpress/internal/messaging"
)

func TestPostgreSQLMessagingReadReceiptsSurviveClockRollback(t *testing.T) {
	for _, action := range []string{"conversation", "read", "read_all"} {
		t.Run(action, func(t *testing.T) {
			ctx, pool := openSetupIntegrationPool(t)
			if err := Migrate(ctx, pool); err != nil {
				t.Fatal(err)
			}
			var users [4]int64
			for i := range users {
				name := fmt.Sprintf("read-clock-%d", i)
				if err := pool.QueryRow(ctx, `INSERT INTO np_users(username,email,password_hash) VALUES($1,$2,'unused-test-hash') RETURNING id`, name, name+"@example.test").Scan(&users[i]); err != nil {
					t.Fatal(err)
				}
			}
			var now time.Time
			if err := pool.QueryRow(ctx, `SELECT now()`).Scan(&now); err != nil {
				t.Fatal(err)
			}
			var fixtures []messaging.Message
			for i := range 7 {
				created := now.Add(time.Duration(24+i) * time.Hour)
				if i == 3 {
					created = now.Add(-time.Hour)
				}
				m := messaging.Message{SenderID: users[0], RecipientID: users[1], Content: fmt.Sprintf("clock fixture %d", i), Nonce: fmt.Sprintf("clock-fixture-nonce-%d", i), CreatedAt: created}
				if i == 1 {
					m.SenderID = users[2]
				}
				if i == 2 {
					m.RecipientID = users[3]
				}
				if i == 4 {
					at := created.Add(time.Hour)
					m.ReadAt = &at
				}
				if err := pool.QueryRow(ctx, `INSERT INTO np_messages(sender_id,recipient_id,content,nonce,created_at,read_at) VALUES($1,$2,$3,$4,$5,$6) RETURNING id`, m.SenderID, m.RecipientID, m.Content, m.Nonce, m.CreatedAt, m.ReadAt).Scan(&m.ID); err != nil {
					t.Fatal(err)
				}
				if _, err := pool.Exec(ctx, `INSERT INTO np_message_notifications(id,user_id,peer_id,created_at,read_at) VALUES($1,$2,$3,$4,$5)`, m.ID, m.RecipientID, m.SenderID, m.CreatedAt, m.ReadAt); err != nil {
					t.Fatal(err)
				}
				fixtures = append(fixtures, m)
			}
			store := NewMessagingRepository(pool)
			apply := func() error {
				if action == "conversation" {
					return store.Read(ctx, users[1], users[0], fixtures[5].ID)
				}
				return store.UpdateNotices(ctx, users[1], fixtures[5].ID, action)
			}
			wantMessages, wantNotices := map[int]bool{4: true}, map[int]bool{4: true}
			if action == "conversation" {
				for _, i := range []int{0, 3, 5} {
					wantMessages[i], wantNotices[i] = true, true
				}
			} else if action == "read" {
				wantNotices[5] = true
			} else {
				for _, i := range []int{0, 1, 3, 5} {
					wantNotices[i] = true
				}
			}
			var first [2][7]*time.Time
			for pass := range 2 {
				if err := apply(); err != nil {
					t.Fatal(err)
				}
				for tableIndex, table := range []string{"np_messages", "np_message_notifications"} {
					want := wantMessages
					if tableIndex == 1 {
						want = wantNotices
					}
					for i, fixture := range fixtures {
						var created time.Time
						var at *time.Time
						if err := pool.QueryRow(ctx, `SELECT created_at,read_at FROM `+table+` WHERE id=$1`, fixture.ID).Scan(&created, &at); err != nil {
							t.Fatal(err)
						}
						if !created.Equal(fixture.CreatedAt) {
							t.Errorf("%s %d changed creation time", table, i)
						}
						if (at != nil) != want[i] {
							t.Errorf("%s %d changed a different account, thread or cursor boundary", table, i)
							continue
						}
						if at == nil {
							continue
						}
						if fixture.ReadAt != nil {
							if !at.Equal(*fixture.ReadAt) {
								t.Errorf("%s replaced an existing read receipt", table)
							}
						} else if created.After(now) {
							if !at.Equal(created) {
								t.Errorf("%s read receipt predates its message: created=%s read=%s", table, created, at)
							}
						} else if at.Before(now) {
							t.Errorf("%s backdated a normal message's new read receipt", table)
						}
						if pass == 0 {
							first[tableIndex][i] = at
						} else if !at.Equal(*first[tableIndex][i]) {
							t.Errorf("%s %d replay replaced its receipt", table, i)
						}
					}
				}
			}
		})
	}
}
