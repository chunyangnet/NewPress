package auth

import (
	"context"
	"newpress.local/newpress/internal/audit"
	"time"
)

func (s *FileUserStore) ListRegistrationCodes(ctx context.Context) ([]RegistrationCode, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return CloneRegistrationCodes(s.registrationCodes), nil
}

func (s *FileUserStore) MutateRegistrationCodes(ctx context.Context, input RegistrationCodeMutation, actor string, now time.Time) ([]RegistrationCode, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	items, affected, err := ApplyRegistrationCodeMutation(s.registrationCodes, input, now)
	if err != nil {
		return nil, err
	}
	event := audit.Event{Action: "registration_codes." + input.Action, ObjectType: "registration_code_registry", ObjectID: "all", Actor: normalizedAuditActor(actor, "system"), Metadata: map[string]any{"count": len(affected)}}
	if err = s.persistCandidateWithCodes(s.users, s.roles, s.nextUserID, s.nextRoleID, now, &event, s.accessTokens, items); err != nil {
		return nil, err
	}
	s.registrationCodes = items
	s.updatedAt = now
	return affected, nil
}
