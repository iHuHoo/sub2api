package repository

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func http2KeepAliveTestPoolSettings() poolSettings {
	return poolSettings{
		maxIdleConns:          10,
		maxIdleConnsPerHost:   5,
		maxConnsPerHost:       10,
		idleConnTimeout:       90 * time.Second,
		responseHeaderTimeout: time.Minute,
	}
}

// 长流 / OpenAI 上游改走 HTTP/2 后，池化连接被代理/NAT 静默掐断会成为“死连接”：
// 两端都以为连接存活，请求落上去会挂到 TCP 重传超时（分钟级）才失败。
// Go HTTP/2 默认不发健康 PING，无法检测这种死连接。
// 必须显式启用主动 PING 探测，让死连接被提前剔除，而不是只靠 ResponseHeaderTimeout
// 事后兜底。
func TestEnableHTTP2KeepAlive_EnablesPingHealthCheck(t *testing.T) {
	tr := &http.Transport{}

	enableHTTP2KeepAlive(tr)
	require.NotNil(t, tr.HTTP2, "必须配置 Go 1.27 原生 HTTP/2 参数")
	require.Positive(t, tr.HTTP2.SendPingTimeout, "必须启用空闲 PING 探测以剔除死连接")
	require.Equal(t, longStreamHTTP2ReadIdleTimeout, tr.HTTP2.SendPingTimeout)
	require.Equal(t, longStreamHTTP2PingTimeout, tr.HTTP2.PingTimeout, "PING 无响应必须有超时判定")
}

// long_stream_h2 模式构建的 Transport 必须带上 H2 PING 健康探测，从源头剔除死连接。
func TestBuildUpstreamTransport_LongStreamH2_EnablesPingHealthCheck(t *testing.T) {
	tr, err := buildUpstreamTransport(http2KeepAliveTestPoolSettings(), nil, upstreamProtocolModeLongStreamH2)
	require.NoError(t, err)
	require.True(t, tr.ForceAttemptHTTP2, "long_stream_h2 必须启用 HTTP/2")
	require.NotNil(t, tr.HTTP2, "long_stream_h2 必须显式配置 HTTP/2 keepalive")
}

// 非 H2 模式（default/h1）不应因本次改动被误配置：default 走 Go 自动 H2（惰性配置，
// 构建时 Protocols/TLSNextProto 仍为空），h1 模式显式禁用 H2。避免波及 Claude/Gemini 热路径。
func TestBuildUpstreamTransport_NonLongStreamH2_NotEagerlyConfigured(t *testing.T) {
	tr, err := buildUpstreamTransport(http2KeepAliveTestPoolSettings(), nil, upstreamProtocolModeDefault)
	require.NoError(t, err)
	require.Nil(t, tr.HTTP2, "default 模式不应主动配置 HTTP/2 keepalive")
}

// long_stream_h2 模式构建的 Transport 必须真正以 HTTP/2 与上游通信，PING 健康探测才有载体：
// 自定义 DialContext 下 Go 不会自动启用 H2，全靠 enableHTTP2KeepAlive 的显式配置。
func TestBuildUpstreamTransport_LongStreamH2_NegotiatesHTTP2(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()

	tr, err := buildUpstreamTransport(http2KeepAliveTestPoolSettings(), nil, upstreamProtocolModeLongStreamH2)
	require.NoError(t, err)
	defer tr.CloseIdleConnections()
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	tr.TLSClientConfig = &tls.Config{RootCAs: roots}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	require.NoError(t, err)
	resp, err := tr.RoundTrip(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, 2, resp.ProtoMajor, "long_stream_h2 必须协商到 HTTP/2")
}

// 死连接在经 HTTP 代理（CONNECT 隧道）时最高发，这是带 proxy 账号的真实生产路径：
// 显式 http2 配置须与 Transport.Proxy 同时正确生效，不能相互干扰。
func TestBuildUpstreamTransport_LongStreamH2_WithHTTPProxy_EnablesKeepAlive(t *testing.T) {
	proxyURL, err := url.Parse("http://127.0.0.1:8080")
	require.NoError(t, err)

	tr, err := buildUpstreamTransport(http2KeepAliveTestPoolSettings(), proxyURL, upstreamProtocolModeLongStreamH2)
	require.NoError(t, err)
	require.True(t, tr.ForceAttemptHTTP2)
	require.NotNil(t, tr.HTTP2, "经代理的 long_stream_h2 也必须启用 HTTP/2 keepalive")
	require.NotNil(t, tr.Proxy, "HTTP 代理仍须通过 Transport.Proxy 生效")
}
