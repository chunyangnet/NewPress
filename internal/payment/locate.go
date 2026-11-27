package payment

import (
	"net/http"
)

// ExtractOrderID only locates a saved order/configuration snapshot. The return
// value is entirely untrusted until Provider.Verify succeeds and its order ID,
// amount, currency, provider and merchant are matched to that saved order.
func ExtractOrderID(provider string, _ http.Header, body []byte) (string, error) {
	var orderID string
	switch provider {
	case "alipay", "alipay_f2f", "epay", "linuxdo":
		v, e := parseForm(body)
		if e != nil {
			return "", e
		}
		key := "out_trade_no"
		orderID = v.Get(key)
	case "twocheckout":
		v, e := parseIPN(body)
		if e != nil || len(v.values["REFNOEXT"]) != 1 {
			return "", ErrRequest
		}
		orderID = v.values.Get("REFNOEXT")
	case "stripe":
		var d struct {
			Data struct {
				Object struct {
					Reference string `json:"client_reference_id"`
				} `json:"object"`
			} `json:"data"`
		}
		if decode(body, &d) != nil {
			return "", ErrRequest
		}
		orderID = d.Data.Object.Reference
	case "paypal":
		var d struct {
			Resource struct {
				CustomID string `json:"custom_id"`
				Units    []struct {
					CustomID string `json:"custom_id"`
				} `json:"purchase_units"`
			} `json:"resource"`
		}
		if decode(body, &d) != nil {
			return "", ErrRequest
		}
		orderID = d.Resource.CustomID
		if orderID == "" && len(d.Resource.Units) == 1 {
			orderID = d.Resource.Units[0].CustomID
		}
	case "wechat":
		// The merchant order number is inside an encrypted resource. Use the
		// order-specific notify_url, or an explicit platform-serial snapshot index.
		return "", ErrUnsupported
	default:
		return "", ErrUnsupported
	}
	if len(orderID) < 1 || len(orderID) > 64 {
		return "", ErrRequest
	}
	for _, c := range orderID {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return "", ErrRequest
		}
	}
	return orderID, nil
}
