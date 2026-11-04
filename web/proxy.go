package web

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

// ProxyConfig defines the trust boundary. ForwardedHeader is empty (ignore),
// "Forwarded", or "X-Forwarded". Empty AllowedHosts permits any valid authority.
// Redirects are root-relative unless their exact origin is explicitly allowed.
type ProxyConfig struct {
	TrustedProxies         []string
	ForwardedHeader        string
	AllowedHosts           []string
	AllowedRedirectOrigins []string
}

// ProxyPolicy is immutable after construction and safe to share across requests.
type ProxyPolicy struct {
	trusted   []netip.Prefix
	family    string
	hosts     map[string]bool
	redirects map[string]bool
}

type requestOriginKey struct{}
type requestOrigin struct {
	ip, scheme, host string
	policies         []*ProxyPolicy
}

// NewProxyPolicy validates configuration without trusting any headers implicitly.
func NewProxyPolicy(config ProxyConfig) (*ProxyPolicy, error) {
	p := &ProxyPolicy{family: config.ForwardedHeader, hosts: map[string]bool{}, redirects: map[string]bool{}}
	if p.family != "" && p.family != "Forwarded" && p.family != "X-Forwarded" {
		return nil, errors.New("web: invalid forwarded header family")
	}
	for _, cidr := range config.TrustedProxies {
		prefix, err := netip.ParsePrefix(cidr)
		if err != nil || prefix.Addr().Is4In6() {
			return nil, fmt.Errorf("web: invalid proxy CIDR %q", cidr)
		}
		p.trusted = append(p.trusted, prefix.Masked())
	}
	for _, host := range config.AllowedHosts {
		canonical, err := canonicalHost(host)
		if err != nil {
			return nil, err
		}
		p.hosts[canonical] = true
	}
	for _, raw := range config.AllowedRedirectOrigins {
		origin, err := canonicalOrigin(raw)
		if err != nil {
			return nil, err
		}
		p.redirects[origin] = true
	}
	return p, nil
}

// NewProxyHandler installs a trust boundary outside arbitrary HTTP middleware.
// The first boundary resolves headers; nested boundaries can only narrow host
// and redirect allowlists, never reinterpret forwarded headers.
func NewProxyHandler(next http.Handler, config ProxyConfig) (http.Handler, error) {
	if next == nil {
		return nil, errors.New("web: HTTP handler cannot be nil")
	}
	p, err := NewProxyPolicy(config)
	if err != nil {
		return nil, err
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request, err := p.prepare(r)
		if err != nil {
			defaultErrorHandler(&Context{Writer: w, Request: r}, err)
			return
		}
		next.ServeHTTP(w, request)
	}), nil
}

func (p *ProxyPolicy) trusts(ip netip.Addr) bool {
	for _, prefix := range p.trusted {
		if prefix.Contains(ip.Unmap()) {
			return true
		}
	}
	return false
}

func (p *ProxyPolicy) prepare(r *http.Request) (*http.Request, error) {
	info, inherited := r.Context().Value(requestOriginKey{}).(requestOrigin)
	if !inherited {
		peer, _ := parseNode(r.RemoteAddr)
		info = requestOrigin{scheme: "http", host: r.Host}
		if peer.IsValid() {
			info.ip = peer.String()
		}
		if r.TLS != nil {
			info.scheme = "https"
		}
		if p.trusts(peer) {
			var err error
			switch p.family {
			case "Forwarded":
				info, err = p.forwarded(r, peer, info)
			case "X-Forwarded":
				info, err = p.xForwarded(r, peer, info)
			}
			if err != nil {
				return nil, NewHTTPError(400, "invalid proxy headers", err)
			}
		}
	}
	host, err := canonicalHost(info.host)
	if err != nil {
		return nil, NewHTTPError(400, "invalid Host", err)
	}
	if len(p.hosts) > 0 && !p.hosts[host] {
		return nil, NewHTTPError(400, "Host is not allowed", nil)
	}
	info.host = host
	info.policies = append(append([]*ProxyPolicy(nil), info.policies...), p)
	request := r.Clone(context.WithValue(r.Context(), requestOriginKey{}, info))
	request.Host = host // Host routes see exactly the validated external authority.
	return request, nil
}

func (p *ProxyPolicy) xForwarded(r *http.Request, peer netip.Addr, info requestOrigin) (requestOrigin, error) {
	if values := r.Header.Values("X-Forwarded-For"); len(values) > 0 {
		chain := strings.Split(strings.Join(values, ","), ",")
		if len(chain) > 32 {
			return info, errors.New("proxy chain is too long")
		}
		for i := len(chain) - 1; i >= 0 && p.trusts(peer); i-- {
			ip, err := parseNode(strings.TrimSpace(chain[i]))
			if err != nil {
				return info, err
			}
			peer = ip
			info.ip = ip.String()
		}
	}
	for _, field := range []struct {
		name   string
		target *string
	}{
		{"X-Forwarded-Proto", &info.scheme}, {"X-Forwarded-Host", &info.host},
	} {
		values := r.Header.Values(field.name)
		if len(values) == 0 {
			continue
		}
		if len(values) != 1 || values[0] == "" || strings.Contains(values[0], ",") {
			return info, errors.New("ambiguous proxy metadata")
		}
		*field.target = values[0]
	}
	if info.scheme != "http" && info.scheme != "https" {
		return info, errors.New("invalid proxy scheme")
	}
	return info, nil
}

func (p *ProxyPolicy) forwarded(r *http.Request, peer netip.Addr, info requestOrigin) (requestOrigin, error) {
	values := r.Header.Values("Forwarded")
	if len(values) == 0 {
		return info, nil
	}
	chain, err := splitForwarded(strings.Join(values, ","), ',')
	if err != nil || len(chain) > 32 {
		return info, errors.New("invalid proxy chain")
	}
	for i := len(chain) - 1; i >= 0 && p.trusts(peer); i-- {
		fields, err := splitForwarded(chain[i], ';')
		if err != nil {
			return info, err
		}
		params := map[string]string{}
		for _, field := range fields {
			name, value, ok := strings.Cut(strings.TrimSpace(field), "=")
			name = strings.ToLower(name)
			if !ok || !httpToken(name) {
				return info, errors.New("invalid Forwarded parameter")
			}
			if _, exists := params[name]; exists {
				return info, errors.New("duplicate Forwarded parameter")
			}
			if strings.HasPrefix(value, "\"") {
				value, err = forwardedQuoted(value)
				if err != nil {
					return info, err
				}
			} else if !httpToken(value) {
				return info, errors.New("invalid Forwarded token")
			}
			params[name] = value
		}
		ip, err := parseNode(params["for"])
		if err != nil {
			return info, err
		}
		peer = ip
		info.ip = ip.String()
		// Metadata belongs to the same hop as the selected client address.
		info.scheme = "http"
		if r.TLS != nil {
			info.scheme = "https"
		}
		info.host = r.Host
		if scheme, ok := params["proto"]; ok {
			if scheme != "http" && scheme != "https" {
				return info, errors.New("invalid proxy scheme")
			}
			info.scheme = scheme
		}
		if host, ok := params["host"]; ok {
			info.host = host
		}
	}
	return info, nil
}

func splitForwarded(raw string, separator byte) ([]string, error) {
	var values []string
	quoted, escaped, start := false, false, 0
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if c < 32 || c == 127 {
			return nil, errors.New("control in Forwarded")
		}
		if escaped {
			escaped = false
			continue
		}
		if quoted && c == '\\' {
			escaped = true
			continue
		}
		if c == '"' {
			quoted = !quoted
		}
		if c == separator && !quoted {
			values = append(values, raw[start:i])
			start = i + 1
		}
	}
	if quoted || escaped {
		return nil, errors.New("unterminated Forwarded quote")
	}
	return append(values, raw[start:]), nil
}

func forwardedQuoted(raw string) (string, error) {
	if len(raw) < 2 || raw[len(raw)-1] != '"' {
		return "", errors.New("invalid Forwarded quote")
	}
	var value strings.Builder
	for i := 1; i < len(raw)-1; i++ {
		c := raw[i]
		switch c {
		case '\\':
			i++
			if i >= len(raw)-1 {
				return "", errors.New("invalid quoted pair")
			}
			c = raw[i]
		case '"':
			return "", errors.New("unescaped quote")
		}
		if c < 32 || c > 126 {
			return "", errors.New("invalid quoted character")
		}
		value.WriteByte(c)
	}
	return value.String(), nil
}

func parseNode(raw string) (netip.Addr, error) {
	if ip, err := netip.ParseAddr(raw); err == nil && ip.Zone() == "" {
		return ip.Unmap(), nil
	}
	host, port, err := net.SplitHostPort(raw)
	if err == nil {
		if _, err = validPort(port); err == nil {
			if ip, err := netip.ParseAddr(host); err == nil && ip.Zone() == "" {
				return ip.Unmap(), nil
			}
		}
	}
	// RFC 7239 also permits a bracketed IPv6 address without a port.
	if strings.HasPrefix(raw, "[") && strings.HasSuffix(raw, "]") {
		if ip, err := netip.ParseAddr(raw[1 : len(raw)-1]); err == nil && ip.Is6() && ip.Zone() == "" {
			return ip.Unmap(), nil
		}
	}
	return netip.Addr{}, errors.New("invalid IP node")
}

func validPort(raw string) (string, error) {
	for _, c := range raw {
		if c < '0' || c > '9' {
			return "", errors.New("invalid port")
		}
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > 65535 {
		return "", errors.New("invalid port")
	}
	return strconv.Itoa(n), nil
}

func canonicalHost(raw string) (string, error) {
	u, err := url.Parse("http://" + raw)
	if err != nil || raw == "" || u.User != nil || u.Host != raw || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("invalid host authority")
	}
	host := strings.ToLower(u.Hostname())
	if ip, err := netip.ParseAddr(host); err == nil {
		if strings.HasPrefix(raw, "[") != ip.Is6() {
			return "", errors.New("invalid IP authority brackets")
		}
		if ip.Zone() != "" {
			return "", errors.New("scoped IP host is not allowed")
		}
		host = ip.String()
		if ip.Is6() {
			host = "[" + host + "]"
		}
	} else {
		if strings.ContainsAny(raw, "[]") {
			return "", errors.New("invalid DNS authority brackets")
		}
		host = strings.TrimSuffix(host, ".")
		if len(host) == 0 || len(host) > 253 {
			return "", errors.New("invalid DNS host")
		}
		for _, label := range strings.Split(host, ".") {
			if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
				return "", errors.New("invalid DNS label")
			}
			for _, c := range label {
				valid := c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-'
				if !valid {
					return "", errors.New("invalid DNS character")
				}
			}
		}
	}
	if port := u.Port(); port != "" {
		canonical, err := validPort(port)
		if err != nil {
			return "", err
		}
		return host + ":" + canonical, nil
	}
	if strings.HasSuffix(raw, ":") {
		return "", errors.New("empty port")
	}
	return host, nil
}

func canonicalOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Opaque != "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", errors.New("invalid HTTP origin")
	}
	if strings.HasSuffix(u.Hostname(), ".") || strings.Contains(raw, "#") {
		return "", errors.New("noncanonical origin")
	}
	host, err := canonicalHost(u.Host)
	if err != nil {
		return "", err
	}
	if u.Scheme == "https" {
		host = strings.TrimSuffix(host, ":443")
	} else {
		host = strings.TrimSuffix(host, ":80")
	}
	return u.Scheme + "://" + host, nil
}

// ClientIP returns the validated client IP, or the immediate peer without a policy.
func (c *Context) ClientIP() string {
	if info, ok := c.Request.Context().Value(requestOriginKey{}).(requestOrigin); ok {
		return info.ip
	}
	ip, err := parseNode(c.Request.RemoteAddr)
	if err != nil {
		return ""
	}
	return ip.String()
}

// Scheme returns the trusted external scheme, otherwise the transport scheme.
func (c *Context) Scheme() string {
	if info, ok := c.Request.Context().Value(requestOriginKey{}).(requestOrigin); ok {
		return info.scheme
	}
	if c.Request.TLS != nil {
		return "https"
	}
	return "http"
}

// Host returns the validated external authority. Raw forwarding headers are not consulted.
func (c *Context) Host() string {
	if info, ok := c.Request.Context().Value(requestOriginKey{}).(requestOrigin); ok {
		return info.host
	}
	host, _ := canonicalHost(c.Request.Host)
	return host
}

func validateRedirect(r *http.Request, location string) error {
	for _, c := range location {
		if c <= 32 || c == 127 || c == '\\' {
			return errors.New("invalid redirect character")
		}
	}
	u, err := url.Parse(location)
	if err != nil || u.User != nil || u.Opaque != "" {
		return errors.New("invalid redirect URL")
	}
	for _, c := range u.Path {
		if c <= 32 || c == 127 || c == '\\' {
			return errors.New("invalid redirect path")
		}
	}
	if u.Scheme == "" && u.Host == "" && strings.HasPrefix(u.Path, "/") && !strings.HasPrefix(u.Path, "//") {
		return nil
	}
	if u.Scheme == "" {
		return errors.New("redirect must be root-relative")
	}
	origin, err := canonicalOrigin(u.Scheme + "://" + u.Host)
	if err != nil {
		return err
	}
	info, ok := r.Context().Value(requestOriginKey{}).(requestOrigin)
	if !ok || len(info.policies) == 0 {
		return errors.New("external redirects are disabled")
	}
	for _, policy := range info.policies {
		if !policy.redirects[origin] {
			return errors.New("redirect origin is not allowed")
		}
	}
	return nil
}
