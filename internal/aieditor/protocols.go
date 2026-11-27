package aieditor

import (
	"net/http"
	"regexp"
	"strings"
)

const (
	ProtocolResponses      = "openai_responses"
	ProtocolAnthropic      = "anthropic"
	ProtocolGeminiText     = "gemini"
	ProtocolAzure          = "azure_openai"
	ProtocolAzureResponses = "azure_responses"
	ProtocolAzureLegacy    = "azure_openai_legacy"
	DefaultAzureAPIVersion = "2024-10-21"
)

func isTextProtocol(protocol string) bool {
	switch protocol {
	case "", ProtocolResponses, ProtocolAnthropic, ProtocolGeminiText, ProtocolAzure, ProtocolAzureResponses, ProtocolAzureLegacy:
		return true
	}
	return false
}

func isImageProtocol(protocol string) bool {
	switch protocol {
	case ProtocolOpenAIImages, ProtocolGemini, ProtocolGeminiInteractions, ProtocolSeedream:
		return true
	}
	return false
}

func validProtocol(protocol string) bool {
	return isTextProtocol(protocol) || isImageProtocol(protocol)
}

var azureAPIVersion = regexp.MustCompile(`^20[0-9]{2}-[0-9]{2}-[0-9]{2}(-preview)?$`)

func normalizeAPIVersion(version, protocol string) (string, error) {
	version = strings.TrimSpace(version)
	if protocol != ProtocolAzureLegacy {
		if version != "" {
			return "", failure("invalid_api_version", "仅 Azure 部署式接口需要填写 API 版本", http.StatusBadRequest)
		}
		return "", nil
	}
	if version == "" {
		version = DefaultAzureAPIVersion
	}
	if !azureAPIVersion.MatchString(version) {
		return "", failure("invalid_api_version", "API 版本格式应为 YYYY-MM-DD 或 YYYY-MM-DD-preview", http.StatusBadRequest)
	}
	return version, nil
}

func validateModel(protocol, model string) error {
	if (protocol == ProtocolGemini || protocol == ProtocolGeminiInteractions || protocol == ProtocolGeminiText) && !geminiModelID.MatchString(strings.TrimPrefix(model, "models/")) {
		return failure("invalid_model", "Gemini 模型 ID 只能包含字母、数字、点、下划线与连字符", http.StatusBadRequest)
	}
	if protocol == ProtocolAzureLegacy && !geminiModelID.MatchString(model) {
		return failure("invalid_model", "Azure 部署名称只能包含字母、数字、点、下划线与连字符", http.StatusBadRequest)
	}
	return nil
}
