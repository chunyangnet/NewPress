package auth

import (
	"context"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ProfileInput is deliberately separate from administrative identity fields.
type ProfileInput struct {
	DisplayName, Locale, Bio string
	AvatarMediaID            int64
}

type ProfileStore interface {
	UpdateProfile(context.Context, int64, UserMutation, ProfileInput) (User, error)
}

func NormalizeProfile(input ProfileInput) (ProfileInput, map[string]string) {
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.Locale = strings.TrimSpace(input.Locale)
	input.Bio = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(input.Bio, "\r\n", "\n"), "\r", "\n"))
	fields := map[string]string{}
	if !validUserText(input.DisplayName, 1, 160) {
		fields["display_name"] = "昵称需为 1–160 字，不能包含控制字符"
	}
	if len(input.Locale) > 35 || !localePattern.MatchString(input.Locale) {
		fields["locale"] = "请填写有效语言标识，例如 zh-CN"
	}
	if !utf8.ValidString(input.Bio) || utf8.RuneCountInString(input.Bio) > 280 {
		fields["bio"] = "简介不能超过 280 字"
	}
	for _, r := range input.Bio {
		if unicode.IsControl(r) && r != '\n' {
			fields["bio"] = "简介不能包含控制字符"
		}
	}
	if input.AvatarMediaID < 0 {
		fields["avatar_media_id"] = "头像编号无效"
	}
	return input, fields
}

func (m *Manager) UpdateProfile(ctx context.Context, actor Principal, version int64, input ProfileInput) (User, error) {
	if m == nil || m.users == nil {
		return User{}, ErrUserStoreNotAvailable
	}
	store, ok := m.users.(ProfileStore)
	if !ok {
		return User{}, ErrUserStoreNotAvailable
	}
	if actor.UserID <= 0 {
		return User{}, ErrUserNotFound
	}
	return store.UpdateProfile(ctx, actor.UserID, UserMutation{ExpectedVersion: version, Actor: actor.Username}, input)
}
