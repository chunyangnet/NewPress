package auth

import (
	"context"
	"time"
)

func (s *FileUserStore) applyReferral(users map[int64]storedUser, id int64, input UserInput, now time.Time) error {
	if input.ReferralHash == "" {
		return nil
	}
	for ownerID, owner := range users {
		if ownerID == id {
			continue
		}
		for _, c := range owner.Referral.Codes {
			if RegistrationCodeHash(c.Code) != input.ReferralHash {
				continue
			}
			d, fact, err := ReferralAttribution(s.userLocked(owner), input.ReferralHash, input.ReferralPolicy, now)
			if err != nil {
				return err
			}
			owner.Referral = d
			users[ownerID] = owner
			u := users[id]
			u.Referral = fact
			users[id] = u
			return nil
		}
	}
	return codeField("invitation_code", "邀请码不存在，请检查分享链接或联系邀请人")
}

func (s *FileUserStore) GenerateReferral(ctx context.Context, id int64, p ReferralPolicy, now time.Time) (ReferralData, error) {
	if err := ctx.Err(); err != nil {
		return ReferralData{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[id]
	if !ok {
		return ReferralData{}, ErrUserNotFound
	}
	d, err := GenerateReferral(s.userLocked(u), p, now)
	if err != nil {
		return d, err
	}
	u.Referral = d
	candidate := cloneStoredUsers(s.users)
	candidate[id] = u
	if err = s.persistCandidate(candidate, s.roles, s.nextUserID, s.nextRoleID, now, nil); err != nil {
		return ReferralData{}, err
	}
	s.users = candidate
	return CloneReferral(d), nil
}
func (s *FileUserStore) SetReferralCode(ctx context.Context, id int64, code string, enabled bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	candidate := cloneStoredUsers(s.users)
	u, ok := candidate[id]
	if !ok {
		return ErrUserNotFound
	}
	found := false
	for i := range u.Referral.Codes {
		if u.Referral.Codes[i].ID == code {
			u.Referral.Codes[i].Enabled = enabled
			u.Referral.Codes[i].Version++
			u.Referral.Codes[i].UpdatedAt = time.Now().UTC()
			found = true
		}
	}
	if !found {
		return ErrUserNotFound
	}
	candidate[id] = u
	if err := s.persistCandidate(candidate, s.roles, s.nextUserID, s.nextRoleID, time.Now().UTC(), nil); err != nil {
		return err
	}
	s.users = candidate
	return nil
}
func (s *FileUserStore) ReferralUsers(ctx context.Context, id int64) ([]User, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []User{}
	for _, u := range s.users {
		if u.Referral.InviterID == id {
			out = append(out, s.userLocked(u))
		}
	}
	return out, nil
}
