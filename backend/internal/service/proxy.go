package service

import (
	"net"
	"net/url"
	"strconv"
	"time"
)

const (
	FallbackModeNone   = "none"
	FallbackModeProxy  = "proxy"
	FallbackModeDirect = "direct"
)

type Proxy struct {
	SupplierUserID         *int64
	AssignedSupplierUserID *int64
	AssignmentValid        bool
	ID                     int64
	Name                   string
	Protocol               string
	Host                   string
	Port                   int
	Username               string
	Password               string
	Status                 string
	CreatedAt              time.Time
	UpdatedAt              time.Time
	ExpiresAt              *time.Time
	FallbackMode           string
	BackupProxyID          *int64
	ExpiryWarnDays         int
}

func (p *Proxy) IsActive() bool {
	return p.Status == StatusActive
}

// IsExpired 报告代理是否已过期（基于 expires_at，与 status 无关）。
func (p *Proxy) IsExpired(now time.Time) bool {
	return p.ExpiresAt != nil && !p.ExpiresAt.After(now)
}

func (p *Proxy) URL() string {
	u := &url.URL{
		Scheme: p.Protocol,
		Host:   net.JoinHostPort(p.Host, strconv.Itoa(p.Port)),
	}
	if p.Username != "" && p.Password != "" {
		u.User = url.UserPassword(p.Username, p.Password)
	}
	return u.String()
}

type ProxyWithAccountCount struct {
	Proxy
	AccountCount   int64
	LatencyMs      *int64
	LatencyStatus  string
	LatencyMessage string
	IPAddress      string
	Country        string
	CountryCode    string
	Region         string
	City           string
	QualityStatus  string
	QualityScore   *int
	QualityGrade   string
	QualitySummary string
	QualityChecked *int64
}

type ProxyAccountSummary struct {
	ID       int64
	Name     string
	Platform string
	Type     string
	Notes    *string
}

// EffectiveSupplierUserID preserves creator ownership independently of assignment.
func (p *Proxy) EffectiveSupplierUserID() *int64 {
	if p.SupplierUserID != nil {
		return p.SupplierUserID
	}
	return p.AssignedSupplierUserID
}
func (p *Proxy) AccessibleToSupplier(id int64) bool {
	if p == nil || id <= 0 {
		return false
	}
	if p.SupplierUserID != nil {
		return *p.SupplierUserID == id
	}
	return p.AssignedSupplierUserID != nil && *p.AssignedSupplierUserID == id && p.AssignmentValid
}
func (p *Proxy) SameOwner(other *Proxy) bool {
	a, b := p.EffectiveSupplierUserID(), other.EffectiveSupplierUserID()
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}
