package httpserver

import (
	"context"
	"net/http"
	"strconv"

	"newpress.local/newpress/internal/assetref"
	"newpress.local/newpress/internal/media"
	"newpress.local/newpress/internal/mediausage"
	"newpress.local/newpress/internal/shop"
)

func (s *Server) holdSettingMedia(w http.ResponseWriter, r *http.Request, values map[string]any) (func(), bool) {
	release, fields := s.mediaUsage.HoldValues(values)
	if len(fields) != 0 {
		release()
		s.writeAPIError(w, r, http.StatusUnprocessableEntity, "media.invalid_reference", "引用的媒体不可用，请重新从媒体库选择", map[string]any{"fields": fields})
		return nil, false
	}
	return release, true
}

func productMediaValues(product shop.Product) map[string]any {
	values := map[string]any{"image": product.Image, "description_source": product.DescriptionSource, "description": product.Description}
	for i, image := range product.Images {
		values["images_"+strconv.Itoa(i)] = image
	}
	for i, image := range product.DetailImages {
		values["detail_images_"+strconv.Itoa(i)] = image
	}
	for i, variant := range product.Variants {
		values["variant_image_"+strconv.Itoa(i)] = variant.Image
	}
	return values
}

// Feature stores are read under mediausage's deletion gate. Writes hold the
// same gate until persistence finishes, including on a stale-save failure.
func (s *Server) settingMediaReferences(ctx context.Context, item media.Item) ([]mediausage.Reference, error) {
	var references []mediausage.Reference
	resolver := assetref.New(s.config.PublicURL)
	add := func(source, key, title string, id int64, values map[string]any) {
		if fields := resolver.Values(values).Matching(item.PublicURL); len(fields) > 0 {
			references = append(references, mediausage.Reference{Source: source, Key: key, Title: title, ID: id, Fields: fields})
		}
	}
	if s.linkPages != nil {
		boards, err := s.linkPages.List(ctx)
		if err != nil {
			return nil, err
		}
		for _, header := range boards {
			board, err := s.linkPages.Get(ctx, header.Slug)
			if err != nil {
				return nil, err
			}
			for _, link := range board.Items {
				add(mediausage.SourceLink, board.Slug, board.Settings.Name+" · "+link.Name, link.ID, map[string]any{"logo_url": link.LogoURL})
			}
		}
	}
	if s.shop != nil {
		catalog, err := s.shop.Catalog(ctx)
		if err != nil {
			return nil, err
		}
		for _, product := range catalog.Products {
			add(mediausage.SourceShop, product.Key, product.Title, 0, productMediaValues(product))
		}
	}
	if s.recognition != nil {
		state, err := s.recognition.Backend.Read(ctx)
		if err != nil {
			return nil, err
		}
		for _, badge := range state.Settings.Badges {
			add(mediausage.SourceBadge, badge.Key, badge.Name, 0, map[string]any{"image": badge.Image})
		}
	}
	return references, nil
}
