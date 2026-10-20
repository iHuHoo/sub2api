package service

import (
	"fmt"
	"gopkg.in/yaml.v3"
	"net"
	"strings"
	"unicode"
)

type ClashExportSet struct {
	Proxies        []Proxy
	SkippedCount   int
	SkippedReasons map[string]int
}

func FilterClashProxies(input []Proxy) ClashExportSet {
	result := ClashExportSet{Proxies: []Proxy{}, SkippedReasons: map[string]int{}}
	seen := map[int64]bool{}
	for _, p := range input {
		valid := p.ID > 0 && !seen[p.ID] && p.Port >= 1 && p.Port <= 65535 && validClashHost(p.Host)
		switch p.Protocol {
		case "http", "https":
		case "socks5", "socks5h":
			valid = valid && (p.Username != "" || p.Password == "") && len(p.Username) <= 255 && len(p.Password) <= 255
		default:
			valid = false
		}
		if !valid {
			result.SkippedCount++
			reason := "INVALID_PROXY"
			if (p.Protocol == "socks5" || p.Protocol == "socks5h") && p.Username == "" && p.Password != "" {
				reason = "SOCKS_AUTH_USERNAME_REQUIRED"
			}
			result.SkippedReasons[reason]++
			continue
		}
		seen[p.ID] = true
		result.Proxies = append(result.Proxies, p)
	}
	return result
}
func validClashHost(host string) bool {
	if net.ParseIP(host) != nil {
		return true
	}
	if len(host) == 0 || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(strings.TrimSuffix(host, "."), ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' {
				return false
			}
		}
	}
	return true
}

type clashNode struct {
	Name     string `yaml:"name"`
	Type     string `yaml:"type"`
	Server   string `yaml:"server"`
	Port     int    `yaml:"port"`
	Username string `yaml:"username,omitempty"`
	Password string `yaml:"password,omitempty"`
	TLS      bool   `yaml:"tls,omitempty"`
	UDP      bool   `yaml:"udp"`
}
type clashGroup struct {
	Name    string   `yaml:"name"`
	Type    string   `yaml:"type"`
	Proxies []string `yaml:"proxies"`
}
type clashConfig struct {
	Mode      string       `yaml:"mode"`
	AllowLAN  bool         `yaml:"allow-lan"`
	MixedPort int          `yaml:"mixed-port"`
	Proxies   []clashNode  `yaml:"proxies"`
	Groups    []clashGroup `yaml:"proxy-groups"`
	Rules     []string     `yaml:"rules"`
}

func RenderClashConfig(set ClashExportSet) ([]byte, error) {
	cfg := clashConfig{Mode: "rule", MixedPort: 7890, Proxies: []clashNode{}, Rules: []string{"MATCH,AoxToken"}}
	names := []string{}
	for _, p := range set.Proxies {
		name := []rune(strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return ' '
			}
			return r
		}, p.Name))
		if len(name) > 80 {
			name = name[:80]
		}
		label := fmt.Sprintf("%s [p%d]", strings.TrimSpace(string(name)), p.ID)
		kind := p.Protocol
		if kind == "https" {
			kind = "http"
		}
		if kind == "socks5h" {
			kind = "socks5"
		}
		cfg.Proxies = append(cfg.Proxies, clashNode{Name: label, Type: kind, Server: p.Host, Port: p.Port, Username: p.Username, Password: p.Password, TLS: p.Protocol == "https"})
		names = append(names, label)
	}
	if len(names) == 0 {
		names = []string{"REJECT"}
	}
	cfg.Groups = []clashGroup{{Name: "AoxToken", Type: "select", Proxies: names}}
	return yaml.Marshal(cfg)
}
