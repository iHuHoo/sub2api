package service

import (
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
	"testing"
)

func TestClashSubscriptionYAMLFilter(t *testing.T) {
	for _, protocol := range []string{"http", "https", "socks5", "socks5h"} {
		t.Run(protocol, func(t *testing.T) {
			p := Proxy{ID: 1, Name: "节点\n: malicious", Protocol: protocol, Host: "2001:db8::1", Port: 1080, Username: "user", Password: "a:\"\nsecret"}
			set := FilterClashProxies([]Proxy{p})
			require.Len(t, set.Proxies, 1)
			data, err := RenderClashConfig(set)
			require.NoError(t, err)
			var doc map[string]any
			require.NoError(t, yaml.Unmarshal(data, &doc))
			require.Equal(t, false, doc["allow-lan"])
			nodes := doc["proxies"].([]any)
			node := nodes[0].(map[string]any)
			require.Equal(t, p.Password, node["password"])
			require.Equal(t, p.Host, node["server"])
			if protocol == "socks5h" {
				require.Equal(t, "socks5", node["type"])
			}
			if protocol == "https" {
				require.Equal(t, true, node["tls"])
				require.NotEqual(t, true, node["skip-cert-verify"])
			}
		})
	}
	for _, protocol := range []string{"http", "https", "socks5", "socks5h", "bogus"} {
		set := FilterClashProxies([]Proxy{{ID: 2, Protocol: protocol, Host: "example.test", Port: 80, Password: "pass"}})
		if protocol == "http" || protocol == "https" {
			require.Len(t, set.Proxies, 1)
		} else {
			require.Empty(t, set.Proxies)
			require.Equal(t, 1, set.SkippedCount)
		}
	}
	data, err := RenderClashConfig(FilterClashProxies(nil))
	require.NoError(t, err)
	require.Contains(t, string(data), "REJECT")
	require.NotContains(t, string(data), "DIRECT")
}
