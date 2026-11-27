package httpserver

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"newpress.local/newpress/internal/commerce"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/resources"
)

func (s *Server) validateResourcePayment(ctx context.Context, o commerce.Order) error {
	var p content.Post
	var err error
	resourceID := ""
	if o.ResourceTerms != nil {
		p, err = s.repository.GetByID(ctx, o.ResourceTerms.PostID)
		resourceID = o.ResourceTerms.Resource
	} else {
		// Legacy orders have only the public path and their hashed price terms.
		u, parseErr := url.Parse(o.ResourceURL)
		if parseErr != nil || u.Host != "" || !strings.HasPrefix(u.Fragment, "resource-") {
			return commerce.ErrResourceDelivery
		}
		resourceID = strings.TrimPrefix(u.Fragment, "resource-")
		switch {
		case strings.HasPrefix(u.Path, "/posts/"):
			p, err = s.repository.GetPublishedBySlug(ctx, strings.TrimSuffix(strings.TrimPrefix(u.Path, "/posts/"), "/"))
		case strings.HasPrefix(u.Path, "/pages/"):
			p, err = s.repository.GetPublishedPageBySlug(ctx, strings.TrimSuffix(strings.TrimPrefix(u.Path, "/pages/"), "/"))
		default:
			return commerce.ErrResourceDelivery
		}
	}
	if errors.Is(err, content.ErrNotFound) {
		return commerce.ErrResourceDelivery
	}
	if err != nil {
		return err
	}
	if !content.PublicAt(p, time.Now()) {
		return commerce.ErrResourceDelivery
	}
	for _, item := range resources.FromMeta(p.Meta).Items {
		if item.ID != resourceID || !resources.Available(item, time.Now()) {
			continue
		}
		f, err := s.resourceFacts(ctx, p, resourceActorID(o.UserID), o.UserID)
		if err != nil {
			return err
		}
		for _, variant := range item.Variants {
			allowed, blocked, groups := s.resourceVersion(ctx, p, item, variant, f)
			for gi, group := range variant.Groups {
				for _, condition := range group.Conditions {
					if !commerce.ResourceMatches(o, resources.Terms(p.ID, item, variant, condition)) {
						continue
					}
					if blocked != "" || !resourceGroupPrerequisites(groups[gi]) {
						return commerce.ErrResourceDelivery
					}
					// Another access path became sufficient before this payment.
					// Preserve the charge for reconciliation instead of selling an
					// already accessible version a second time.
					if allowed && !s.resourceCheck(ctx, p, item, variant, condition, f) {
						return commerce.ErrResourceDelivery
					}
					day := resources.Day(f.now, s.siteSnapshot(ctx).Settings.Identity.Timezone)
					daily, total := resources.Counts(f.state, f.actor, p.ID, item.ID, day)
					if variant.Kind == "download" && resources.Remaining(item, daily, total) == 0 {
						return commerce.ErrResourceDelivery
					}
					return nil
				}
			}
		}
	}
	return commerce.ErrResourceDelivery
}
