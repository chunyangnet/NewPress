package media

import (
	"net/http"
)

func classify(prefix []byte) (string, string, string, error) {
	mediaType := http.DetectContentType(prefix)
	switch mediaType {
	case "image/jpeg":
		return mediaType, ".jpg", KindImage, nil
	case "image/png":
		return mediaType, ".png", KindImage, nil
	case "image/gif":
		return mediaType, ".gif", KindImage, nil
	case "image/webp":
		return mediaType, ".webp", KindImage, nil
	case "application/pdf":
		return mediaType, ".pdf", KindDocument, nil
	case "audio/mpeg":
		return mediaType, ".mp3", KindAudio, nil
	case "audio/ogg", "application/ogg":
		return "audio/ogg", ".ogg", KindAudio, nil
	case "audio/wave", "audio/wav", "audio/x-wav":
		return "audio/wav", ".wav", KindAudio, nil
	case "video/mp4":
		return mediaType, ".mp4", KindVideo, nil
	case "video/webm":
		return mediaType, ".webm", KindVideo, nil
	default:
		return "", "", "", ErrUnsupportedType
	}
}

func webPDimensions(prefix []byte) (int, int, error) {
	if len(prefix) < 30 || string(prefix[:4]) != "RIFF" || string(prefix[8:12]) != "WEBP" {
		return 0, 0, ErrInvalidInput
	}
	switch string(prefix[12:16]) {
	case "VP8X":
		width := 1 + int(prefix[24]) + int(prefix[25])<<8 + int(prefix[26])<<16
		height := 1 + int(prefix[27]) + int(prefix[28])<<8 + int(prefix[29])<<16
		return width, height, nil
	case "VP8L":
		if len(prefix) < 25 || prefix[20] != 0x2f {
			return 0, 0, ErrInvalidInput
		}
		bits := uint32(prefix[21]) | uint32(prefix[22])<<8 | uint32(prefix[23])<<16 | uint32(prefix[24])<<24
		return int(bits&0x3fff) + 1, int((bits>>14)&0x3fff) + 1, nil
	case "VP8 ":
		if len(prefix) < 30 || prefix[23] != 0x9d || prefix[24] != 0x01 || prefix[25] != 0x2a {
			return 0, 0, ErrInvalidInput
		}
		width := int(prefix[26]) | int(prefix[27]&0x3f)<<8
		height := int(prefix[28]) | int(prefix[29]&0x3f)<<8
		return width, height, nil
	default:
		return 0, 0, ErrInvalidInput
	}
}

func classifyStoredMIME(mediaType string) (string, string, string, error) {
	switch mediaType {
	case "image/jpeg":
		return mediaType, ".jpg", KindImage, nil
	case "image/png":
		return mediaType, ".png", KindImage, nil
	case "image/gif":
		return mediaType, ".gif", KindImage, nil
	case "image/webp":
		return mediaType, ".webp", KindImage, nil
	case "application/pdf":
		return mediaType, ".pdf", KindDocument, nil
	case "audio/mpeg":
		return mediaType, ".mp3", KindAudio, nil
	case "audio/ogg":
		return mediaType, ".ogg", KindAudio, nil
	case "audio/wav":
		return mediaType, ".wav", KindAudio, nil
	case "video/mp4":
		return mediaType, ".mp4", KindVideo, nil
	case "video/webm":
		return mediaType, ".webm", KindVideo, nil
	default:
		return "", "", "", ErrUnsupportedType
	}
}
