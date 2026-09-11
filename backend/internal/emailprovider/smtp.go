package emailprovider

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

// Client is one configured relay account, ready to send. Safe for
// concurrent use: every Send dials its own connection and tears it down
// again (SMTP relays are not commonly kept warm across incidents that may
// be minutes or hours apart, and this repo's dispatch volume — a single
// operator's own alerts — never approaches a scale where connection reuse
// would matter).
type Client struct {
	cfg Config
}

// NewClient validates cfg and returns a ready sender.
func NewClient(cfg Config) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.Port == 0 {
		cfg.Port = defaultPort
	}
	return &Client{cfg: cfg}, nil
}

// phase names where in the SMTP exchange a failure happened — used only to
// classify the failure and to build a Reason string, never echoed from
// anything the server sent.
type phase string

const (
	phaseConnect  phase = "connect"
	phaseStartTLS phase = "starttls"
	phaseAuth     phase = "auth"
	phaseMail     phase = "mail"
	phaseRcpt     phase = "rcpt"
	phaseData     phase = "data"
)

// Send performs one real SMTP delivery attempt of msg to to, over a fresh
// connection: dial, EHLO, an opportunistic (or, for Config.ImplicitTLS,
// upfront) TLS upgrade, AUTH if configured, MAIL FROM, RCPT TO, DATA, QUIT.
// It does not retry — that policy lives in alerting.EmailDispatcher, the
// same split ADR-0006/ADR-0007 already draw between "one provider attempt"
// and "the caller's retry loop".
//
// ctx bounds the whole exchange: its deadline (or, absent one, a short
// internal default) is applied to the underlying connection once, up front,
// rather than threaded through each protocol step individually — net/smtp's
// Client type predates context and has no per-call ctx parameter, so a
// connection-level deadline is the real mechanism available, the same
// approach a context-bounded database/sql driver uses under the hood.
func (c *Client) Send(ctx context.Context, to string, msg Message) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(30 * time.Second)
	}

	addr := net.JoinHostPort(c.cfg.Host, strconv.Itoa(c.port()))
	dialer := &net.Dialer{}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return classify(phaseConnect, err)
	}
	if err := conn.SetDeadline(deadline); err != nil {
		_ = conn.Close()
		return newSendError(KindTransient, "smtp connection could not set a deadline")
	}

	if c.cfg.ImplicitTLS {
		conn = tls.Client(conn, c.tlsConfig())
	}

	smtpClient, err := smtp.NewClient(conn, c.cfg.Host)
	if err != nil {
		_ = conn.Close()
		return classify(phaseConnect, err)
	}
	defer func() { _ = smtpClient.Close() }()

	if !c.cfg.ImplicitTLS {
		if ok, _ := smtpClient.Extension("STARTTLS"); ok {
			if err := smtpClient.StartTLS(c.tlsConfig()); err != nil {
				return classify(phaseStartTLS, err)
			}
		}
	}

	if c.cfg.Username != "" {
		auth := smtp.PlainAuth("", c.cfg.Username, c.cfg.Password, c.cfg.Host)
		if err := smtpClient.Auth(auth); err != nil {
			return classify(phaseAuth, err)
		}
	}

	if err := smtpClient.Mail(c.cfg.From); err != nil {
		return classify(phaseMail, err)
	}
	if err := smtpClient.Rcpt(to); err != nil {
		return classify(phaseRcpt, err)
	}

	w, err := smtpClient.Data()
	if err != nil {
		return classify(phaseData, err)
	}
	if _, err := w.Write(buildMessage(c.cfg.From, to, msg, time.Now())); err != nil {
		_ = w.Close()
		return classify(phaseData, err)
	}
	if err := w.Close(); err != nil {
		return classify(phaseData, err)
	}

	// A failed QUIT does not undo an already-accepted DATA — the message is
	// sent as far as this protocol can promise, so this is deliberately not
	// treated as a send failure.
	_ = smtpClient.Quit()
	return nil
}

func (c *Client) port() int {
	if c.cfg.Port != 0 {
		return c.cfg.Port
	}
	return defaultPort
}

func (c *Client) tlsConfig() *tls.Config {
	return &tls.Config{ServerName: c.cfg.Host, RootCAs: c.cfg.RootCAs}
}

// sanitizeHeaderValue strips CR and LF from a value before it is written
// into an RFC 5322 header field, so an embedded CRLF in caller-supplied
// input (Message.Subject, primarily) cannot inject an additional header
// line or terminate the header block early. It is deliberately a strip,
// not a reject-and-error: a rendered subject line is not this package's
// job to validate at the caller's expense, only to make impossible to turn
// into a different email than the one it renders as.
func sanitizeHeaderValue(v string) string {
	v = strings.ReplaceAll(v, "\r", "")
	v = strings.ReplaceAll(v, "\n", "")
	return v
}

// buildMessage renders msg as a minimal, valid RFC 5322 message: headers,
// a blank line, then the body, entirely CRLF-terminated (RFC 5321's DATA
// command requires it).
//
// Every header value is passed through sanitizeHeaderValue first. to has
// already passed smtp.Client.Rcpt's own line-injection check (Send calls
// Rcpt before this) and from is operator configuration, not per-message
// input, but msg.Subject is caller-supplied per send — Client.Send and
// Message are this package's exported API, and a Subject containing an
// embedded CR/LF could otherwise inject arbitrary additional headers (a
// forged Bcc, a spoofed From) or terminate the header block early: the
// classic email header-injection vulnerability class. Sanitizing every
// header value uniformly here, at the one place that actually writes them,
// is the defense — not trusting each call site to have done it.
func buildMessage(from, to string, msg Message, sentAt time.Time) []byte {
	var b strings.Builder
	b.WriteString("From: " + sanitizeHeaderValue(from) + "\r\n")
	b.WriteString("To: " + sanitizeHeaderValue(to) + "\r\n")
	b.WriteString("Subject: " + sanitizeHeaderValue(msg.Subject) + "\r\n")
	b.WriteString("Date: " + sentAt.Format(time.RFC1123Z) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=\"utf-8\"\r\n")
	b.WriteString("\r\n")
	b.WriteString(strings.ReplaceAll(msg.Body, "\n", "\r\n"))
	b.WriteString("\r\n")
	return []byte(b.String())
}

// classify maps one failed protocol step onto ErrorKind. A *textproto.Error
// is a real SMTP reply the relay sent: RFC 5321's own convention is that a
// 4xx reply means "try again", a 5xx reply means "don't" — reused here
// exactly as ADR-0006 already reuses HTTP's own 4xx/5xx convention for
// webhooks. Which Kind a 5xx becomes still depends on phase: a 5xx from the
// relay rejecting our own AUTH is never the recipient's fault (KindCredential);
// a 5xx from RCPT TO/MAIL FROM/DATA is the SMTP-protocol shape of a hard
// bounce (KindPermanent).
//
// Anything that is not a *textproto.Error — a dial failure, a read/write
// timeout, a connection reset, or (during AUTH specifically) net/smtp's own
// refusal to send credentials over an unencrypted, non-localhost connection
// — is a transport/configuration problem, never a claim about the
// recipient address, and its Reason is always a fixed string: never this
// package's own recipient/credential values, and never the underlying
// error's own Error() text, which for a dial failure can embed the relay's
// address.
func classify(p phase, err error) *SendError {
	var tpErr *textproto.Error
	if errors.As(err, &tpErr) {
		switch {
		case tpErr.Code >= 500:
			if p == phaseAuth {
				return newSendError(KindCredential, "smtp AUTH rejected by relay (code %d)", tpErr.Code)
			}
			return newSendError(KindPermanent, "smtp %s rejected (code %d)", p, tpErr.Code)
		case tpErr.Code >= 400:
			return newSendError(KindTransient, "smtp %s temporarily failed (code %d)", p, tpErr.Code)
		default:
			return newSendError(KindTransient, "smtp %s returned unexpected code %d", p, tpErr.Code)
		}
	}
	if p == phaseAuth {
		return newSendError(KindCredential, "smtp authentication could not proceed (relay rejected it, or the connection is not encrypted)")
	}
	return newSendError(KindTransient, "smtp %s transport error", p)
}
