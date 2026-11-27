package aieditor

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

type imageTransport func(*http.Request) (*http.Response, error)

func (f imageTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func imageResponse(body string) *http.Response {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}
}
func imageFixture(t *testing.T) ([]byte, string) {
	t.Helper()
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewNRGBA(image.Rect(0, 0, 20, 16))); err != nil {
		t.Fatal(err)
	}
	return data.Bytes(), base64.StdEncoding.EncodeToString(data.Bytes())
}

func TestImageAdaptersRequestAndResponse(t *testing.T) {
	raw, encoded := imageFixture(t)
	for _, protocol := range []string{ProtocolOpenAIImages, ProtocolGemini, ProtocolGeminiInteractions, ProtocolSeedream} {
		t.Run(protocol, func(t *testing.T) {
			client := &ImageClient{HTTP: &http.Client{Transport: imageTransport(func(r *http.Request) (*http.Response, error) {
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				if r.Method != "POST" || r.URL.RawQuery != "" {
					t.Fatal("incorrect request", r.Method, r.URL)
				}
				google := protocol == ProtocolGemini || protocol == ProtocolGeminiInteractions
				if google && (r.Header.Get("x-goog-api-key") != "test-image-key" || r.Header.Get("Authorization") != "") {
					t.Error("Gemini auth is not isolated")
				}
				if !google && r.Header.Get("Authorization") != "Bearer test-image-key" {
					t.Error("Bearer auth missing")
				}
				switch protocol {
				case ProtocolGemini:
					if r.URL.Path != "/v1beta/models/picture-model:generateContent" {
						t.Error(r.URL.Path)
					}
					config := payload["generationConfig"].(map[string]any)["imageConfig"].(map[string]any)
					if config["aspectRatio"] != "3:2" || config["imageSize"] != "2K" {
						t.Error(config)
					}
					return imageResponse(`{"candidates":[{"content":{"parts":[{"thought":true,"inlineData":{"mimeType":"image/png","data":"not-a-real-thought-image"}},{"inlineData":{"mimeType":"image/png","data":"` + encoded + `"}}]}}]}`), nil
				case ProtocolGeminiInteractions:
					if r.URL.Path != "/v1beta/interactions" || payload["store"] != false {
						t.Error("interactions request", payload)
					}
					format := payload["response_format"].([]any)[0].(map[string]any)
					if format["type"] != "image" || format["aspect_ratio"] != "3:2" || format["image_size"] != "2K" {
						t.Error(format)
					}
					return imageResponse(`{"outputs":[{"type":"text","text":"completed"},{"type":"image","mime_type":"image/png","data":"` + encoded + `"}]}`), nil
				case ProtocolSeedream:
					if r.URL.Path != "/api/v3/images/generations" || payload["size"] != "2496x1664" || payload["watermark"] != true || payload["sequential_image_generation"] != "disabled" || payload["n"] != nil {
						t.Error(payload, r.URL.Path)
					}
				default:
					if r.URL.Path != "/v1/images/generations" || payload["size"] != "1536x1024" || payload["quality"] != "high" || payload["response_format"] != nil {
						t.Error(payload, r.URL.Path)
					}
				}
				return imageResponse(`{"data":[{"b64_json":"` + encoded + `"}]}`), nil
			})}}
			generation := ImageGeneration{Prompt: "书店，暖色灯光", AspectRatio: "3:2", Resolution: "2K"}
			if protocol == ProtocolOpenAIImages {
				generation.Resolution = ""
				generation.Quality = "high"
			}
			images, err := client.Generate(t.Context(), Connection{Protocol: protocol, BaseURL: "https://images.example.test", APIKey: "test-image-key", Model: "picture-model"}, generation)
			if err != nil || len(images) != 1 || !bytes.Equal(images[0].Data, raw) || images[0].Extension != "png" {
				t.Fatal("adapter failed", images, err)
			}
		})
	}
}

func TestImageDownloadsNeverForwardCredentialsAndRejectPrivateURLs(t *testing.T) {
	raw, _ := imageFixture(t)
	address, downloads := "https://cdn.example.test/picture.png?signature=test", 0
	client := &ImageClient{
		HTTP: &http.Client{Transport: imageTransport(func(*http.Request) (*http.Response, error) {
			data, _ := json.Marshal(map[string]any{"data": []any{map[string]string{"url": address}}})
			return imageResponse(string(data)), nil
		})},
		Download: &http.Client{Transport: imageTransport(func(r *http.Request) (*http.Response, error) {
			downloads++
			if r.Header.Get("Authorization") != "" || r.Header.Get("x-goog-api-key") != "" || r.URL.Host != "cdn.example.test" {
				t.Fatal("credential forwarded to image host")
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(raw))}, nil
		})},
	}
	connection := Connection{Protocol: ProtocolOpenAIImages, BaseURL: "https://images.example.test", APIKey: "secret", Model: "gpt-image-1"}
	if _, err := client.Generate(t.Context(), connection, ImageGeneration{Prompt: "image"}); err != nil || downloads != 1 {
		t.Fatal(err, downloads)
	}
	for _, blocked := range []string{"http://cdn.example.test/a", "https://127.0.0.1/a", "https://192.168.1.1/a", "https://[::1]/a", "https://user:password@cdn.example.test/a"} {
		address = blocked
		if _, err := client.Generate(t.Context(), connection, ImageGeneration{Prompt: "image"}); err == nil || downloads != 1 {
			t.Fatal("unsafe download", blocked, err)
		}
	}
	production := NewImageClient()
	if production.Download.CheckRedirect(&http.Request{}, nil) != http.ErrUseLastResponse || production.HTTP.CheckRedirect(&http.Request{}, nil) != http.ErrUseLastResponse {
		t.Fatal("redirects must not carry image keys")
	}
}

func TestImageErrorsAndCancellation(t *testing.T) {
	connection := Connection{Protocol: ProtocolOpenAIImages, BaseURL: "https://images.example.test", APIKey: "private-key", Model: "gpt-image-1"}
	for _, body := range []string{`{"data":[]}`, `{"data":[{"b64_json":"!!!"}]}`, `{"data":[{"b64_json":"PHN2Zy8+"}]}`, `<html>private-key</html>`, `{"error":{"message":"private-key"}}`} {
		client := &ImageClient{HTTP: &http.Client{Transport: imageTransport(func(*http.Request) (*http.Response, error) { return imageResponse(body), nil })}}
		if _, err := client.Generate(t.Context(), connection, ImageGeneration{Prompt: "image"}); err == nil || strings.Contains(err.Error(), "private-key") {
			t.Fatal("invalid payload accepted or key leaked", err)
		}
	}
	calls := 0
	client := &ImageClient{HTTP: &http.Client{Transport: imageTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}}
	for _, generation := range []ImageGeneration{{Prompt: ""}, {Prompt: "image", AspectRatio: "16:9"}, {Prompt: "image", Resolution: "4K"}, {Prompt: "image", Quality: "unsupported"}} {
		if _, err := client.Generate(t.Context(), connection, generation); err == nil || calls != 0 {
			t.Fatal("invalid request reached provider", err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := client.Generate(ctx, connection, ImageGeneration{Prompt: "image"}); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost", err)
	}
}

func TestImageSettingsPersistenceAndProtocolSeparation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ai-settings.json")
	store, err := NewSettingsStore(path)
	if err != nil {
		t.Fatal(err)
	}
	input := store.Snapshot()
	input.Providers = []Provider{{ID: "images", Name: "Nano", Protocol: ProtocolGemini, BaseURL: "https://generativelanguage.googleapis.com", APIKey: "private-image-key", Models: []string{"gemini-2.5-flash-image"}}}
	saved, err := store.Save(input)
	if err != nil || saved.DefaultProvider != "" || saved.DefaultImageProvider != "images" || saved.Providers[0].APIKey != "" {
		t.Fatal(saved, err)
	}
	reopened, err := NewSettingsStore(path)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := reopened.ResolveImage(Connection{ProviderID: "images"})
	if err != nil || resolved.Protocol != ProtocolGemini || resolved.APIKey != "private-image-key" || resolved.BaseURL != "https://generativelanguage.googleapis.com/v1beta" {
		t.Fatal(resolved, err)
	}
	if _, err := reopened.Resolve(Connection{ProviderID: "images"}, true); err == nil {
		t.Fatal("image key used for chat")
	}
	if _, err := reopened.ResolveImage(Connection{ProviderID: "images", Protocol: ProtocolOpenAIImages}); err == nil {
		t.Fatal("saved protocol overridden")
	}
	saved.Providers[0].Protocol = ProtocolGeminiInteractions
	if _, err := reopened.Save(saved); err == nil {
		t.Fatal("protocol changed without fresh key")
	}
	if _, err := (Connection{Protocol: ProtocolGemini, BaseURL: "https://images.example.test", APIKey: "secret", Model: "../../models/other"}).Validate(true); err == nil {
		t.Fatal("Gemini model path traversal")
	}
}

func TestImageModelDiscoveryKeepsImageModalities(t *testing.T) {
	for _, protocol := range []string{ProtocolOpenAIImages, ProtocolGemini} {
		client := &Client{HTTP: &http.Client{Transport: imageTransport(func(r *http.Request) (*http.Response, error) {
			if protocol == ProtocolGemini {
				if r.Header.Get("x-goog-api-key") != "secret" {
					t.Error("wrong models auth")
				}
				return imageResponse(`{"models":[{"name":"models/gemini-2.5-flash-image","displayName":"Nano"},{"name":"models/gemini-text"},{"name":"models/imagen-4"}]}`), nil
			}
			return imageResponse(`{"data":[{"id":"picture","output_modalities":["image"]},{"id":"text","output_modalities":["text"]}]}`), nil
		})}}
		models, err := client.Models(t.Context(), Connection{Protocol: protocol, BaseURL: "https://images.example.test", APIKey: "secret"})
		if err != nil || len(models) != 1 {
			t.Fatal(models, err)
		}
	}
}
