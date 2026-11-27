package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/HugoSmits86/nativewebp"
	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp"

	"newpress.local/newpress/internal/packagefs"
)

const (
	MaxProcessBytes     = 32 << 20
	MaxProcessPixels    = 16_000_000
	MaxProcessDimension = 8192
	maxDerivativeBytes  = 16 << 20
	imageRecipe         = 1
)

var (
	ErrImageUnsupported = errors.New("image processing requires a static JPEG, PNG or WebP")
	ErrImageBudget      = errors.New("image exceeds processing budget")
	ErrImageBusy        = errors.New("another image is being processed")
)

// Fixed value structures keep Item snapshots independent and comparable. Empty
// image state is omitted for objects without generated derivatives.
type ImageSet struct {
	Recipe      int             `json:"recipe"`
	FocalX      int             `json:"focal_x"`
	FocalY      int             `json:"focal_y"`
	Width       int             `json:"width"`
	Height      int             `json:"height"`
	GeneratedAt time.Time       `json:"generated_at"`
	Variants    [6]ImageVariant `json:"variants"`
}

type ImageVariant struct {
	Name   string      `json:"name"`
	Width  int         `json:"width"`
	Height int         `json:"height"`
	Native ImageObject `json:"native"`
	WebP   ImageObject `json:"webp"`
}

type ImageObject struct {
	ObjectKey      string `json:"object_key"`
	MIMEType       string `json:"mime_type"`
	SizeBytes      int64  `json:"size_bytes"`
	ChecksumSHA256 string `json:"checksum_sha256"`
}

type ImageInput struct {
	FocalX int `json:"focal_x"`
	FocalY int `json:"focal_y"`
}

var imageSizes = [...]struct {
	name          string
	width, height int
	crop          bool
}{
	{"square", 320, 320, true}, {"card", 640, 360, true}, {"card-large", 1280, 720, true},
	{"small", 640, 640, false}, {"medium", 1280, 1280, false}, {"large", 1920, 1920, false},
}

func (item Item) CanProcessImage() error {
	if item.Status() != StatusActive {
		return ErrTrashed
	}
	if item.MIMEType != "image/jpeg" && item.MIMEType != "image/png" && item.MIMEType != "image/webp" {
		return ErrImageUnsupported
	}
	if item.SizeBytes > MaxProcessBytes || item.Width > MaxProcessDimension || item.Height > MaxProcessDimension || int64(item.Width)*int64(item.Height) > MaxProcessPixels {
		return ErrImageBudget
	}
	return nil
}

func (item Item) Variant(name string) (ImageVariant, bool) {
	if item.Image.Recipe == 0 {
		return ImageVariant{}, false
	}
	for _, variant := range item.Image.Variants {
		if variant.Name == name {
			return variant, true
		}
	}
	return ImageVariant{}, false
}

func (item Item) VariantURL(name, format string) string {
	if _, ok := item.Variant(name); !ok {
		return ""
	}
	q := url.Values{"variant": {name}}
	if format != "" {
		q.Set("format", format)
	}
	return item.PublicURL + "?" + q.Encode()
}

func (variant ImageVariant) Object(format string) (ImageObject, bool) {
	switch format {
	case "webp":
		return variant.WebP, true
	case "native":
		return variant.Native, true
	case "":
		if variant.WebP.SizeBytes < variant.Native.SizeBytes {
			return variant.WebP, true
		}
		return variant.Native, true
	default:
		return ImageObject{}, false
	}
}

// Objects includes the original and every indexed derivative for backup,
// restoration and deletion. These object descriptors must not be saved as Items.
func (item Item) Objects() []ImageObject {
	objects := []ImageObject{{ObjectKey: item.ObjectKey, MIMEType: item.MIMEType, SizeBytes: item.SizeBytes, ChecksumSHA256: item.ChecksumSHA256}}
	if item.Image.Recipe == 0 {
		return objects
	}
	for _, variant := range item.Image.Variants {
		objects = append(objects, variant.Native, variant.WebP)
	}
	return objects
}

func (object ImageObject) descriptor() Item {
	return Item{ObjectKey: object.ObjectKey, MIMEType: object.MIMEType, SizeBytes: object.SizeBytes, ChecksumSHA256: object.ChecksumSHA256}
}

func (item Item) imageObject(variant ImageVariant, object ImageObject) Item {
	item.ObjectKey, item.ChecksumSHA256, item.MIMEType, item.SizeBytes = object.ObjectKey, object.ChecksumSHA256, object.MIMEType, object.SizeBytes
	item.Width, item.Height = variant.Width, variant.Height
	item.Filename = strings.TrimSuffix(item.Filename, filepath.Ext(item.Filename)) + "-" + variant.Name + filepath.Ext(object.ObjectKey)
	// No date validator: regeneration can change bytes twice in one second.
	item.CreatedAt = time.Time{}
	item.Image = ImageSet{}
	return item
}

func (m *Manager) OpenVariant(id int64, name, format string, includeTrash bool) (Item, ReadSeekCloser, error) {
	if m == nil || id <= 0 {
		return Item{}, nil, ErrNotFound
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	item, ok := m.items[id]
	if !ok || (!includeTrash && item.Status() == StatusTrash) {
		return Item{}, nil, ErrNotFound
	}
	variant, ok := item.Variant(name)
	if !ok {
		return Item{}, nil, ErrNotFound
	}
	object, ok := variant.Object(format)
	if !ok {
		return Item{}, nil, ErrInvalidInput
	}
	return m.openObjectLocked(item.imageObject(variant, object))
}

func (m *Manager) ProcessImage(ctx context.Context, id, expectedVersion int64, input ImageInput) (Item, error) {
	if m == nil {
		return Item{}, ErrNotFound
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return Item{}, err
	}
	if input.FocalX < 0 || input.FocalX > 100 || input.FocalY < 0 || input.FocalY > 100 {
		return Item{}, fmt.Errorf("%w: focal coordinates must be integers from 0 to 100", ErrInvalidInput)
	}
	current, err := m.Get(id)
	if err != nil {
		return Item{}, err
	}
	if current.Version != expectedVersion || expectedVersion <= 0 {
		return current, ErrVersionConflict
	}
	if err := current.CanProcessImage(); err != nil {
		return current, err
	}
	if !m.processing.tryAcquire(current) {
		return current, ErrImageBusy
	}
	defer m.processing.release(current)
	_, reader, err := m.Open(id)
	if err != nil {
		return current, err
	}
	payload, readErr := io.ReadAll(io.LimitReader(contextReader{context: ctx, reader: reader}, MaxProcessBytes+1))
	closeErr := reader.Close()
	if readErr != nil {
		return current, readErr
	}
	if closeErr != nil {
		return current, closeErr
	}
	digest := sha256.Sum256(payload)
	if int64(len(payload)) != current.SizeBytes || hex.EncodeToString(digest[:]) != current.ChecksumSHA256 {
		return current, ErrObjectCorrupt
	}
	orientation, err := imageOrientation(payload, current.MIMEType)
	if err != nil {
		return current, err
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(payload))
	if err != nil || config.Width != current.Width || config.Height != current.Height {
		return current, fmt.Errorf("%w: image dimensions differ from index", ErrInvalidInput)
	}
	source, format, err := image.Decode(contextReader{context: ctx, reader: bytes.NewReader(payload)})
	if contextErr := ctx.Err(); contextErr != nil {
		return current, contextErr
	}
	if err != nil {
		return current, fmt.Errorf("%w: image cannot be decoded", ErrInvalidInput)
	}
	source = orientImage(NormalizeDecodedColors(source, format), orientation)
	updated := current
	updated.Image = ImageSet{Recipe: imageRecipe, FocalX: input.FocalX, FocalY: input.FocalY, Width: source.Bounds().Dx(), Height: source.Bounds().Dy(), GeneratedAt: time.Now().UTC()}
	type stagedImage struct {
		upload *stagedUpload
		object Item
	}
	staged := make([]stagedImage, 0, 12)
	defer func() {
		for _, entry := range staged {
			entry.upload.discard()
		}
	}()
	// Request-local reuse: the source and encoding recipe are fixed here. Each
	// variant still gets independent staging and its original object namespace.
	completed := make(map[imageGeometry][2]stagedImage, len(imageSizes))
	for i, spec := range imageSizes {
		if err := ctx.Err(); err != nil {
			return current, err
		}
		geometry := imageResizeGeometry(source.Bounds(), spec.width, spec.height, spec.crop, input)
		previous, reused := completed[geometry]
		var resized *image.NRGBA
		if !reused {
			resized = resizeImage(source, spec.width, spec.height, spec.crop, input)
		}
		variant := ImageVariant{Name: spec.name, Width: geometry.size.X, Height: geometry.size.Y}
		var generated [2]stagedImage
		for formatIndex, format := range []string{"native", "webp"} {
			if err := ctx.Err(); err != nil {
				return current, err
			}
			var upload *stagedUpload
			var mime string
			if reused {
				mime = previous[formatIndex].object.MIMEType
				upload, err = m.copyImageStage(ctx, previous[formatIndex].upload)
			} else {
				var encoded []byte
				encoded, mime, err = encodeImage(resized, format)
				if err == nil {
					upload, err = m.stageUpload(ctx, bytes.NewReader(encoded), maxDerivativeBytes)
				}
			}
			if err != nil {
				return current, err
			}
			sha := hex.EncodeToString(upload.digest[:])
			_, extension, _, _ := classifyStoredMIME(mime)
			object := ImageObject{ObjectKey: derivativeKey(id, spec.name, sha, extension), MIMEType: mime, SizeBytes: upload.size, ChecksumSHA256: sha}
			if format == "webp" {
				variant.WebP = object
			} else {
				variant.Native = object
			}
			generated[formatIndex] = stagedImage{upload, current.imageObject(variant, object)}
			staged = append(staged, generated[formatIndex])
		}
		if !reused {
			completed[geometry] = generated
		}
		updated.Image.Variants[i] = variant
	}
	if err := ctx.Err(); err != nil {
		return current, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	latest, exists := m.items[id]
	if !exists {
		return Item{}, ErrNotFound
	}
	if latest.Version != expectedVersion {
		return latest, ErrVersionConflict
	}
	if latest.Status() != StatusActive {
		return latest, ErrTrashed
	}
	created := []Item{}
	committed := false
	defer func() {
		if !committed {
			for _, object := range created {
				_ = m.removeObjectLocked(object)
			}
		}
	}()
	for _, entry := range staged {
		if err := ctx.Err(); err != nil {
			return current, err
		}
		createdObject, err := m.commitObject(entry.upload, entry.object)
		if err != nil {
			return current, err
		}
		if createdObject {
			created = append(created, entry.object)
		}
	}
	updated, err = m.saveStateLocked(current, updated)
	if err != nil {
		return current, err
	}
	committed = true
	if err := m.cleanupDerivedLocked(id); err != nil {
		slog.Warn("clean retired image derivatives", "id", id, "error", err)
	}
	return updated, nil
}

// copyImageStage never consumes the original stage: commitObject may rename a
// disk stage later, so each variant must own a separate temporary file.
func (m *Manager) copyImageStage(ctx context.Context, source *stagedUpload) (*stagedUpload, error) {
	reader, err := source.open()
	if err != nil {
		return nil, err
	}
	copy, err := m.stageUpload(ctx, reader, maxDerivativeBytes)
	closeErr := reader.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		copy.discard()
		return nil, closeErr
	}
	if copy.size != source.size || copy.digest != source.digest {
		copy.discard()
		return nil, ErrObjectCorrupt
	}
	return copy, nil
}

func derivativeKey(id int64, name, checksum, extension string) string {
	return "objects/derived/" + strconv.FormatInt(id, 10) + "/" + name + "-" + checksum + extension
}

type imageGeometry struct {
	source image.Rectangle
	size   image.Point
}

func imageResizeGeometry(bounds image.Rectangle, width, height int, crop bool, focal ImageInput) imageGeometry {
	w, h := bounds.Dx(), bounds.Dy()
	if crop {
		cropW, cropH := w, h
		if w*height > h*width {
			cropW = max(1, h*width/height)
		} else {
			cropH = max(1, w*height/width)
		}
		x := min(w-cropW, max(0, w*focal.FocalX/100-cropW/2))
		y := min(h-cropH, max(0, h*focal.FocalY/100-cropH/2))
		bounds = image.Rect(bounds.Min.X+x, bounds.Min.Y+y, bounds.Min.X+x+cropW, bounds.Min.Y+y+cropH)
		w, h = cropW, cropH
	}
	scale := min(1.0, min(float64(width)/float64(w), float64(height)/float64(h)))
	return imageGeometry{source: bounds, size: image.Pt(max(1, int(float64(w)*scale+0.5)), max(1, int(float64(h)*scale+0.5)))}
}

func resizeImage(source image.Image, width, height int, crop bool, focal ImageInput) *image.NRGBA {
	geometry := imageResizeGeometry(source.Bounds(), width, height, crop, focal)
	bounds := geometry.source
	dst := image.NewNRGBA(image.Rectangle{Max: geometry.size})
	// Keep the original conversion/rounding behavior for transparent and
	// high-bit-depth images. Only known opaque 8-bit sources bypass the scaler.
	opaque := false
	if dst.Bounds().Size() == bounds.Size() {
		switch src := source.(type) {
		case *image.YCbCr:
			opaque = true
		case *image.NRGBA:
			opaque = src.Opaque()
		case *image.RGBA:
			opaque = src.Opaque()
		}
	}
	if opaque {
		draw.Draw(dst, dst.Bounds(), source, bounds.Min, draw.Src)
	} else {
		xdraw.CatmullRom.Scale(dst, dst.Bounds(), source, bounds, draw.Src, nil)
	}
	return dst
}

func encodeImage(source *image.NRGBA, format string) ([]byte, string, error) {
	var buffer bytes.Buffer
	mime := "image/webp"
	var err error
	if format == "webp" {
		err = nativewebp.Encode(&buffer, source, &nativewebp.Options{CompressionLevel: nativewebp.BestSpeed})
	} else if source.Opaque() {
		mime = "image/jpeg"
		// Opaque NRGBA and RGBA have identical pixel bytes. A read-only view
		// selects jpeg's specialized RGBA path without copying pixels or allocating
		// a color interface for every pixel. Preserve subimage bounds and stride.
		opaque := &image.RGBA{Pix: source.Pix, Stride: source.Stride, Rect: source.Rect}
		err = jpeg.Encode(&buffer, opaque, &jpeg.Options{Quality: 84})
	} else {
		mime = "image/png"
		err = png.Encode(&buffer, source)
	}
	if err != nil {
		return nil, "", err
	}
	if buffer.Len() > maxDerivativeBytes {
		return nil, "", ErrImageBudget
	}
	return buffer.Bytes(), mime, nil
}

func validateImageSet(item Item) error {
	set := item.Image
	if set.Recipe == 0 {
		if set != (ImageSet{}) {
			return errors.New("image state without recipe")
		}
		return nil
	}
	if set.Recipe != imageRecipe || item.Kind != KindImage || set.GeneratedAt.IsZero() || set.GeneratedAt.Before(item.CreatedAt) || set.GeneratedAt.After(item.UpdatedAt) || set.FocalX < 0 || set.FocalX > 100 || set.FocalY < 0 || set.FocalY > 100 {
		return errors.New("invalid image recipe metadata")
	}
	if !((set.Width == item.Width && set.Height == item.Height) || (set.Width == item.Height && set.Height == item.Width)) || set.Width > MaxProcessDimension || set.Height > MaxProcessDimension || int64(set.Width)*int64(set.Height) > MaxProcessPixels || item.SizeBytes > MaxProcessBytes {
		return errors.New("invalid processed image dimensions")
	}
	if item.MIMEType != "image/jpeg" && item.MIMEType != "image/png" && item.MIMEType != "image/webp" {
		return errors.New("unsupported processed image type")
	}
	for i, variant := range set.Variants {
		spec := imageSizes[i]
		if variant.Name != spec.name || variant.Width <= 0 || variant.Height <= 0 || variant.Width > spec.width || variant.Height > spec.height || variant.Width > set.Width || variant.Height > set.Height {
			return errors.New("invalid image variant dimensions")
		}
		if variant.Native.MIMEType != "image/jpeg" && variant.Native.MIMEType != "image/png" {
			return errors.New("invalid native image type")
		}
		if variant.WebP.MIMEType != "image/webp" {
			return errors.New("invalid WebP image type")
		}
		for _, object := range []ImageObject{variant.Native, variant.WebP} {
			checksum, err := hex.DecodeString(object.ChecksumSHA256)
			_, extension, _, mimeErr := classifyStoredMIME(object.MIMEType)
			if err != nil || len(checksum) != 32 || strings.ToLower(object.ChecksumSHA256) != object.ChecksumSHA256 || mimeErr != nil || object.SizeBytes <= 0 || object.SizeBytes > maxDerivativeBytes || object.ObjectKey != derivativeKey(item.ID, variant.Name, object.ChecksumSHA256, extension) {
				return errors.New("invalid image variant object")
			}
		}
	}
	return nil
}

// Only our derived namespace is swept, after the current index has loaded or
// committed. Originals and unknown filenames are never garbage-collected.
func (m *Manager) cleanupDerivedLocked(id int64) error {
	referenced := map[string]bool{}
	for _, item := range m.items {
		for _, object := range item.Objects() {
			referenced[object.ObjectKey] = true
		}
	}
	if m.root == "" {
		for key := range m.memoryObjects {
			if strings.HasPrefix(key, "objects/derived/") && !referenced[key] {
				delete(m.memoryObjects, key)
			}
		}
		return nil
	}
	base := filepath.Join(m.root, "objects", "derived")
	if _, err := packagefs.LstatNoSymlink(base); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	directories, err := os.ReadDir(base)
	if err != nil {
		return err
	}
	for _, directory := range directories {
		owner, parseErr := strconv.ParseInt(directory.Name(), 10, 64)
		if parseErr != nil || owner <= 0 || strconv.FormatInt(owner, 10) != directory.Name() || (id != 0 && id != owner) {
			continue
		}
		folder := filepath.Join(base, directory.Name())
		if info, err := packagefs.LstatNoSymlink(folder); err != nil || !info.IsDir() {
			return errors.New("unsafe image derivative directory")
		}
		entries, err := os.ReadDir(folder)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			key := "objects/derived/" + directory.Name() + "/" + entry.Name()
			if referenced[key] || !generatedImageFilename(entry.Name()) {
				continue
			}
			if err := m.removeObjectLocked(Item{ObjectKey: key}); err != nil {
				return err
			}
		}
	}
	return nil
}

func generatedImageFilename(name string) bool {
	extension := filepath.Ext(name)
	if extension != ".jpg" && extension != ".png" && extension != ".webp" {
		return false
	}
	for _, spec := range imageSizes {
		checksum := strings.TrimSuffix(strings.TrimPrefix(name, spec.name+"-"), extension)
		if strings.HasPrefix(name, spec.name+"-") && len(checksum) == 64 {
			if _, err := hex.DecodeString(checksum); err == nil {
				return true
			}
		}
	}
	return false
}
