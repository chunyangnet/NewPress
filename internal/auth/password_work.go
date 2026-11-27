package auth

import "context"

// Password work shares one small budget across login, registration, recovery
// and administrator mutations. Do not queue HTTP requests behind Argon2: an
// abandoned burst must not keep consuming CPU after its clients have left.
func (m *Manager) acquirePasswordWork(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case m.verificationCh <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-m.verificationCh
			return err
		}
		return nil
	default:
		if err := ctx.Err(); err != nil {
			return err
		}
		return ErrPasswordBusy
	}
}

func (m *Manager) hashPassword(ctx context.Context, password string) (string, error) {
	if err := m.acquirePasswordWork(ctx); err != nil {
		return "", err
	}
	defer func() { <-m.verificationCh }()
	hash, err := HashPassword(password)
	// Argon2 itself is not cancellable; keep its slot until it returns, then
	// reject cancellation before any account or recovery-token mutation.
	if contextErr := ctx.Err(); contextErr != nil {
		return "", contextErr
	}
	if err != nil {
		return "", &UserValidationError{Fields: map[string]string{"password": "密码至少需要 12 个 Unicode 字符，且不能超过 1024 字节"}}
	}
	return hash, nil
}

func (m *Manager) verifyPassword(ctx context.Context, hash, password string) (bool, error) {
	if err := m.acquirePasswordWork(ctx); err != nil {
		return false, err
	}
	defer func() { <-m.verificationCh }()
	valid, err := VerifyPassword(hash, password)
	if contextErr := ctx.Err(); contextErr != nil {
		return false, contextErr
	}
	return valid, err
}
