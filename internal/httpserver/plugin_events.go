package httpserver

import (
	"context"
	"errors"
	"time"

	"newpress.local/newpress/internal/activity"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/pluginjobs"
)

// ObservePluginActivity joins the existing BeforeAck chain, never competing for
// source ACKs. Only identifier-only supported events cross the plugin boundary.
func (s *Server) ObservePluginActivity(ctx context.Context, event activity.Event) error {
	kind := ""
	switch event.Kind {
	case activity.ContentUpdated:
		kind = "content.updated"
	case activity.Publication:
		kind = "content.published"
	case activity.PostModeration:
		kind = "content.moderated"
	case activity.CommentPublic:
		kind = "comment.published"
	default:
		return nil
	}
	if err := event.Validate(); err != nil {
		return err
	}
	receipts, ok := s.pluginTasks.(pluginjobs.FanoutStore)
	if !ok || s.plugins == nil {
		return errors.New("plugin event storage unavailable")
	}
	delivery := pluginjobs.Event{Key: event.Key, Kind: kind, ObjectID: event.ObjectID, Version: event.Version, At: event.At}
	if reader, ok := receipts.(pluginjobs.FanoutReader); ok {
		_, found, err := reader.FindFanout(ctx, delivery)
		if err != nil {
			return err
		}
		if found {
			// Re-read through DeliverFrozen so restoration cancellation and the
			// existing receipt remain authoritative; do not rediscover recipients.
			return pluginjobs.DeliverFrozen(ctx, receipts, s.pluginTasks, delivery, nil, time.Now().UTC())
		}
	}
	discovered, err := s.plugins.TaskSubscriptions(ctx)
	if err != nil {
		return err
	}
	subscriptions := []pluginjobs.Subscription{}
	for _, subscription := range discovered {
		if subscription.ActorID <= 0 || event.At.Before(subscription.Since) {
			continue
		}
		user, err := s.auth.GetUser(ctx, subscription.ActorID)
		if errors.Is(err, auth.ErrUserNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if !pluginTaskUserAllowed(user) {
			continue
		}
		subscriptions = append(subscriptions, subscription)
	}
	return pluginjobs.DeliverFrozen(ctx, receipts, s.pluginTasks, delivery, subscriptions, time.Now().UTC())
}
