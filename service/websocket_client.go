package service

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/gorilla/websocket"
	"golang.org/x/net/proxy"
)

// GetWebSocketDialerWithProxy builds a WebSocket dialer using the same proxy
// semantics as the relay HTTP client. An empty proxy preserves the default
// dialer behavior.
func GetWebSocketDialerWithProxy(rawProxyURL string) (*websocket.Dialer, error) {
	dialer := *websocket.DefaultDialer
	dialer.HandshakeTimeout = 30 * time.Second
	trimmed := strings.TrimSpace(rawProxyURL)
	if trimmed == "" {
		return &dialer, nil
	}
	parsedURL, _, err := common.ParseProxyURLRuntime(trimmed)
	if err != nil {
		return nil, err
	}
	if parsedURL == nil {
		return &dialer, nil
	}
	switch parsedURL.Scheme {
	case "http", "https":
		dialer.Proxy = http.ProxyURL(parsedURL)
	case "socks5", "socks5h":
		forwardDialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
		proxyDialer, proxyErr := proxy.FromURL(parsedURL, forwardDialer)
		if proxyErr != nil {
			return nil, proxyErr
		}
		contextDialer, ok := proxyDialer.(proxy.ContextDialer)
		if !ok {
			return nil, fmt.Errorf("SOCKS proxy dialer does not support context cancellation")
		}
		dialer.Proxy = nil
		dialer.NetDialContext = contextDialer.DialContext
	default:
		return nil, fmt.Errorf("unsupported websocket proxy scheme")
	}
	return &dialer, nil
}
