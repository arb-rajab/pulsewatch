package emailprovider

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"mime"
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
	// Message and Client.Send are this package's exported API: to and
	// msg.Subject are both caller-supplied per send (unlike c.cfg.From,
	// fixed relay configuration), and both end up in an RFC 5322 header
	// line (buildMessage, below). Rejecting an embedded CR/LF in either
	// here, before any network I/O happens — the same reject-don't-
	// silently-mangle shape net/smtp.Client's own validateLine already
	// uses internally for these same two values — is what stops either
	// from injecting an arbitrary additional header (a forged Bcc) or
	// terminating the header block early: the classic email
	// header-injection vulnerability class. Subject is additionally
	// MIME-encoded (RFC 2047) in buildMessage, which is structurally
	// incapable of producing a raw CR/LF in its output regardless of
	// input — belt and braces on top of this reject.
	if strings.ContainsAny(to, "\r\n") {
		return newSendError(KindPermanent, "recipient address must not contain CR or LF")
	}
	if strings.ContainsAny(msg.Subject, "\r\n") {
		return newSendError(KindPermanent, "message subject must not contain CR or LF")
	}

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
	// CodeQL (go/email-injection) flags this write because to/msg.Subject/
	// msg.Body are exported-API parameters it cannot prove sanitized by
	// tracing into buildMessage's own body. They are, by this point:
	// - to and msg.Subject were already rejected above if they contain a
	//   CR or LF, the same reject-not-mangle shape net/smtp.Client's own
	//   validateLine uses internally for these two values, and to is
	//   independently re-validated by smtpClient.Rcpt just above.
	// - buildMessage renders Subject via RFC 2047 MIME encoding
	//   (mime.QEncoding.Encode) and Body via RFC 2045
	//   Content-Transfer-Encoding: base64 — both structural guarantees:
	//   neither output alphabet can contain a raw CR, LF, or a bare "."
	//   line, so neither can inject a header or prematurely terminate the
	//   DATA block, regardless of input content.
	// Each of these is exercised by a dedicated regression test that
	// attempts the actual injection and asserts it is blocked
	// (TestClient_SendRejectsSubjectHeaderInjection,
	// TestClient_SendRejectsRecipientHeaderInjection,
	// TestBuildMessage_SanitizesHeaderValues,
	// TestBuildMessage_BodyIsBase64EncodedAndRoundTrips, in
	// smtp_test.go) — verified false positive, not an unexamined
	// suppression.
	// codeql[go/email-injection]
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

// sanitizeHeaderValue strips any CR/LF a value still carries before it is
// written into an RFC 5322 header field. Send already rejects a Subject
// containing one outright (the primary guard, applied before any network
// I/O happens); to has separately already passed smtp.Client.Rcpt's own
// line-injection check by the time this runs; from is fixed relay
// configuration, not per-message input. This is the second, redundant
// layer for the same header-injection vulnerability class — belt and
// braces at the one place every header value is actually written, not a
// substitute for Send's own reject.
func sanitizeHeaderValue(v string) string {
	v = strings.ReplaceAll(v, "\r", "")
	v = strings.ReplaceAll(v, "\n", "")
	return v
}

// buildMessage renders msg as a minimal, valid RFC 5322 message: headers,
// a blank line, then the body, entirely CRLF-terminated (RFC 5321's DATA
// command requires it). Every plain header value still passes through
// sanitizeHeaderValue — see its own doc comment for why that is a second
// layer, not the only one. Subject additionally goes through RFC 2047
// MIME encoding (mime.QEncoding.Encode): a real, structural guarantee
// against header injection, not just a heuristic one — the encoded-word
// form Q/B-encodes every byte outside a narrow printable-ASCII allowlist,
// so a raw CR or LF cannot survive into the rendered header line
// regardless of what Subject contained (and, as a real bonus, it is also
// what correctly represents a non-ASCII subject at all, which naive
// concatenation never did).
func buildMessage(from, to string, msg Message, sentAt time.Time) []byte {
	var b strings.Builder
	b.WriteString("From: " + sanitizeHeaderValue(from) + "\r\n")
	b.WriteString("To: " + sanitizeHeaderValue(to) + "\r\n")
	b.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", sanitizeHeaderValue(msg.Subject)) + "\r\n")
	b.WriteString("Date: " + sentAt.Format(time.RFC1123Z) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=\"utf-8\"\r\n")
	b.WriteString("Content-Transfer-Encoding: base64\r\n")
	b.WriteString("\r\n")
	b.WriteString(encodeBodyBase64(msg.Body))
	b.WriteString("\r\n")
	return []byte(b.String())
}

// encodeBodyBase64 renders Body as base64 (RFC 2045 Content-Transfer-
// Encoding: base64, wrapped at the standard 76 characters per line). Like
// Subject's RFC 2047 encoding above, this is a structural guarantee, not a
// heuristic one: base64's output alphabet is a fixed 65-character set that
// cannot contain a raw CR, LF, or a bare "." line — so a Body cannot smuggle
// an extra header, a forged Cc, or a premature end to the SMTP DATA block
// (a bare "." line) regardless of what it contains, without depending on
// net/textproto's own dot-stuffing (DotWriter, still in effect underneath
// this as defense in depth) being the only thing standing between an
// attacker-influenced Body and the raw wire. Body is legitimately
// multi-line free text — the one Message field this package cannot reject
// or strip newlines from without breaking the feature — so encoding its
// transfer representation, not its content, is what keeps it both safe and
// unrestricted.
func encodeBodyBase64(body string) string {
	encoded := base64.StdEncoding.EncodeToString([]byte(body))
	const lineLen = 76
	var b strings.Builder
	for i := 0; i < len(encoded); i += lineLen {
		end := i + lineLen
		if end > len(encoded) {
			end = len(encoded)
		}
		b.WriteString(encoded[i:end])
		b.WriteString("\r\n")
	}
	return strings.TrimSuffix(b.String(), "\r\n")
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
