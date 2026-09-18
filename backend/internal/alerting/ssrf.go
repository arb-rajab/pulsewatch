package alerting

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"time"
)

// This file is T-08's fix (06-security-threat-model.md): a webhook
// destination is a URL an operator (or an attacker who has stolen the
// operator's session) supplies, and WebhookDispatcher makes a real outbound
// HTTP request to it. Left unvalidated, that request can be pointed at
// anything reachable from the backend's own network position — an internal
// admin panel, another tenant's service in a shared VPC, or a cloud
// provider's instance-metadata endpoint (169.254.169.254 on AWS/GCP/Azure),
// which typically requires no credential at all to read and can itself hand
// back real cloud credentials. That is the classic SSRF-via-webhook chain.
//
// This guard runs at two points, deliberately, not one:
//
//   - ValidateWebhookURL, at channel-creation/secret-rotation time
//     (operatorapi.CreateAlertChannel / RotateAlertChannelSecret): a
//     same-request rejection with a useful 422, so an operator seeing a
//     private-looking destination get rejected finds out immediately,
//     not when the next incident silently fails to notify.
//   - guardedDialContext, at the moment of the real TCP dial, on every
//     WebhookDispatcher attempt (including retries): the only check that
//     actually matters for DNS rebinding. A hostname that resolved to a
//     public address at creation time can be repointed at a private one by
//     the time WebhookDispatcher.Dispatch runs — the attacker's own DNS
//     server is free to answer differently on the second lookup — so
//     creation-time validation alone is not a real control, only a
//     convenience check. guardedDialContext re-resolves and re-validates on
//     every single connection attempt and dials the validated address
//     directly (never the hostname a second time), closing that gap.

// ErrWebhookDestinationBlocked is returned (wrapped) whenever a webhook
// destination is refused because it names, or resolves to, a private,
// loopback, link-local, or otherwise disallowed address.
var ErrWebhookDestinationBlocked = errors.New("webhook destination resolves to a private, loopback, link-local, or otherwise disallowed address")

// ValidateWebhookURL checks destination well-formedness and, best-effort,
// whether it already names a disallowed address. It is not the real
// enforcement point (guardedDialContext is) — a hostname that resolves fine
// right now can still be blocked at dispatch time, and a hostname that
// fails to resolve right now (a typo, a host that doesn't exist yet, or —
// in this project's own CI/sandbox network — no outbound DNS at all) is not
// itself treated as a reason to refuse configuring the channel, since
// guardedDialContext will enforce the real check when it actually matters.
func ValidateWebhookURL(ctx context.Context, destination string) error {
	u, err := url.Parse(destination)
	if err != nil {
		return fmt.Errorf("%w: not a valid URL", ErrWebhookDestinationBlocked)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w: scheme must be http or https", ErrWebhookDestinationBlocked)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("%w: missing host", ErrWebhookDestinationBlocked)
	}

	if ip := net.ParseIP(host); ip != nil {
		if blockedIP(ip) {
			return fmt.Errorf("%w: %s", ErrWebhookDestinationBlocked, ip)
		}
		return nil
	}

	lookupCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(lookupCtx, host)
	if err != nil {
		// Not itself a rejection reason — see the doc comment above.
		return nil
	}
	for _, addr := range addrs {
		if blockedIP(addr.IP) {
			return fmt.Errorf("%w: %s resolves to %s", ErrWebhookDestinationBlocked, host, addr.IP)
		}
	}
	return nil
}

// blockedIP is the actual range list: loopback (127.0.0.0/8, ::1),
// link-local (169.254.0.0/16, fe80::/10 — the range that includes every
// cloud provider's instance-metadata endpoint, 169.254.169.254 named
// explicitly since it's the specific, high-value target this exists to
// stop), RFC 1918/RFC 4193 private ranges, the unspecified address, and
// multicast. Nothing here is scoped to a particular cloud provider — the
// range is the control, not a hardcoded metadata IP.
func blockedIP(ip net.IP) bool {
	return ip.IsLoopback() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsPrivate() ||
		ip.IsUnspecified() ||
		ip.IsMulticast()
}

// guardedDialContext wraps a *net.Dialer so it re-resolves and re-validates
// the target host on every single call — i.e. on every real WebhookDispatcher
// connection attempt, including retries — and then dials the validated IP
// address directly rather than handing the hostname back to the standard
// library dialer for a second, independent resolution. That second point
// matters as much as the re-validation itself: without it, an attacker's DNS
// server could answer this function's lookup with a safe address and the
// dialer's own subsequent lookup with a private one (a TOCTOU window even
// narrower than classic DNS rebinding), and the request would still reach
// the private target.
func guardedDialContext(dialer *net.Dialer) func(ctx context.Context, network, addr string) (net.Conn, error) {
	if dialer == nil {
		dialer = &net.Dialer{Timeout: 10 * time.Second}
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("webhook dial target %q: %w", addr, err)
		}

		var candidates []net.IP
		if ip := net.ParseIP(host); ip != nil {
			candidates = []net.IP{ip}
		} else {
			addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, fmt.Errorf("resolve webhook host %q: %w", host, err)
			}
			for _, a := range addrs {
				candidates = append(candidates, a.IP)
			}
		}
		if len(candidates) == 0 {
			return nil, fmt.Errorf("resolve webhook host %q: no addresses found", host)
		}
		for _, ip := range candidates {
			if blockedIP(ip) {
				return nil, fmt.Errorf("%w: %s", ErrWebhookDestinationBlocked, ip)
			}
		}
		return dialer.DialContext(ctx, network, net.JoinHostPort(candidates[0].String(), port))
	}
}
