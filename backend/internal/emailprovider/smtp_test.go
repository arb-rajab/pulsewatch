package emailprovider

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"errors"
	"io"
	"math/big"
	"net"
	"net/textproto"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// These tests exercise the real Client.Send against a real TCP listener
// speaking the real SMTP wire protocol (RFC 5321, plus STARTTLS/RFC 3207 and
// AUTH PLAIN/RFC 4616) — the same "a real protocol exchange against a
// server that speaks it, not a mocked Send" bar internal/pushprovider's own
// fcm_test.go/apns_test.go set for Session 18's push providers.

var (
	testCertOnce sync.Once
	testCert     tls.Certificate
	testCertPool *x509.CertPool
)

// generateTestCert builds a real, self-signed TLS certificate for
// "127.0.0.1" (every test server here listens on 127.0.0.1), generated once
// per test binary — the same pattern pushdispatch_test.go's pushRSAKey
// uses for its own once-generated test key. Deliberately takes no *testing.T:
// this is called from background server goroutines that can still be
// running after the test that started them has returned (a client that
// already got its answer doesn't block on the server noticing), and calling
// a *testing.T method from such a goroutine is its own race independent of
// anything this package's own code does.
func generateTestCert() (tls.Certificate, *x509.CertPool) {
	testCertOnce.Do(func() {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		template := &x509.Certificate{
			SerialNumber: big.NewInt(1),
			Subject:      pkix.Name{CommonName: "127.0.0.1"},
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().Add(24 * time.Hour),
			KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		}
		der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
		if err != nil {
			panic(err)
		}
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			panic(err)
		}
		testCert = tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
		testCertPool = x509.NewCertPool()
		testCertPool.AddCert(cert)
	})
	return testCert, testCertPool
}

// smtpReply is one scripted server response to a command.
type smtpReply struct {
	code int
	msg  string
}

// fakeSMTPServer is a minimal, real SMTP server: a real net.Listener, a real
// line-oriented protocol exchange over net/textproto (the same package
// net/smtp's own Client uses on the wire), real numeric reply codes. Each
// hook defaults to accepting (a 250/235/354 reply) when unset, so a test
// only configures the one step it cares about.
type fakeSMTPServer struct {
	ln net.Listener

	implicitTLS   bool // if true, every accepted conn is TLS from byte one
	offerSTARTTLS bool
	offerAUTH     bool

	mailReply func(attempt int, from string) smtpReply
	rcptReply func(attempt int, to string) smtpReply
	dataReply func(attempt int) smtpReply
	authReply func(user, pass string) smtpReply

	attempts atomic.Int32

	mu       sync.Mutex
	lastData []byte
}

// newFakeSMTPServer opens the listener but does not yet accept connections —
// call start() once every field a test wants to configure
// (offerSTARTTLS/offerAUTH/implicitTLS/*Reply) has been set. Splitting
// construction from start is deliberate: those fields are read by the
// serve()/handleConn() goroutines with no synchronization (matching how
// mockFCM/fcmServer's own handler funcs work in this repo's push tests),
// which is only race-free if nothing else writes them after the accept loop
// is running.
func newFakeSMTPServer(t *testing.T) *fakeSMTPServer {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &fakeSMTPServer{ln: ln}
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

func (s *fakeSMTPServer) start() {
	go s.serve()
}

func (s *fakeSMTPServer) addr() (host string, port int) {
	tcpAddr := s.ln.Addr().(*net.TCPAddr)
	return "127.0.0.1", tcpAddr.Port
}

func (s *fakeSMTPServer) serve() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		if s.implicitTLS {
			cert, _ := generateTestCert()
			conn = tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cert}})
		}
		go s.handleConn(conn)
	}
}

func (s *fakeSMTPServer) handleConn(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	attempt := int(s.attempts.Add(1))
	tp := textproto.NewConn(conn)
	tlsActive := s.implicitTLS

	_ = tp.PrintfLine("220 fake.test ESMTP ready")

	for {
		line, err := tp.ReadLine()
		if err != nil {
			return
		}
		upper := strings.ToUpper(line)

		switch {
		case strings.HasPrefix(upper, "EHLO"):
			s.writeEHLO(tp, tlsActive)
		case strings.HasPrefix(upper, "HELO"):
			_ = tp.PrintfLine("250 fake.test")
		case strings.HasPrefix(upper, "STARTTLS"):
			if !s.offerSTARTTLS || tlsActive {
				_ = tp.PrintfLine("502 command not implemented")
				continue
			}
			_ = tp.PrintfLine("220 go ahead")
			cert, _ := generateTestCert()
			tlsConn := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cert}})
			if err := tlsConn.HandshakeContext(context.Background()); err != nil {
				return
			}
			conn = tlsConn
			tp = textproto.NewConn(conn)
			tlsActive = true
		case strings.HasPrefix(upper, "AUTH PLAIN"):
			s.handleAuth(tp, line)
		case strings.HasPrefix(upper, "MAIL FROM:"):
			reply := smtpReply{250, "OK"}
			if s.mailReply != nil {
				reply = s.mailReply(attempt, line)
			}
			_ = tp.PrintfLine("%d %s", reply.code, reply.msg)
		case strings.HasPrefix(upper, "RCPT TO:"):
			reply := smtpReply{250, "OK"}
			if s.rcptReply != nil {
				reply = s.rcptReply(attempt, line)
			}
			_ = tp.PrintfLine("%d %s", reply.code, reply.msg)
		case strings.HasPrefix(upper, "DATA"):
			reply := smtpReply{354, "go ahead"}
			if s.dataReply != nil {
				reply = s.dataReply(attempt)
			}
			_ = tp.PrintfLine("%d %s", reply.code, reply.msg)
			if reply.code != 354 {
				continue
			}
			data, _ := io.ReadAll(tp.DotReader())
			s.mu.Lock()
			s.lastData = data
			s.mu.Unlock()
			_ = tp.PrintfLine("250 message accepted")
		case strings.HasPrefix(upper, "QUIT"):
			_ = tp.PrintfLine("221 bye")
			return
		default:
			_ = tp.PrintfLine("500 unrecognized command")
		}
	}
}

func (s *fakeSMTPServer) writeEHLO(tp *textproto.Conn, tlsActive bool) {
	lines := []string{"fake.test at your service"}
	if s.offerSTARTTLS && !tlsActive {
		lines = append(lines, "STARTTLS")
	}
	if s.offerAUTH {
		lines = append(lines, "AUTH PLAIN")
	}
	for i, l := range lines {
		if i == len(lines)-1 {
			_ = tp.PrintfLine("250 %s", l)
		} else {
			_ = tp.PrintfLine("250-%s", l)
		}
	}
}

func (s *fakeSMTPServer) handleAuth(tp *textproto.Conn, line string) {
	fields := strings.Fields(line)
	var b64 string
	if len(fields) >= 3 {
		b64 = fields[2]
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		_ = tp.PrintfLine("501 malformed AUTH PLAIN response")
		return
	}
	parts := strings.SplitN(string(raw), "\x00", 3)
	if len(parts) != 3 {
		_ = tp.PrintfLine("501 malformed AUTH PLAIN response")
		return
	}
	user, pass := parts[1], parts[2]

	reply := smtpReply{235, "authentication successful"}
	if s.authReply != nil {
		reply = s.authReply(user, pass)
	}
	_ = tp.PrintfLine("%d %s", reply.code, reply.msg)
}

func (s *fakeSMTPServer) sentBody() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.lastData)
}

// baseConfig returns a Config pointed at srv, with no TLS/auth — the
// plaintext, no-auth-required baseline most tests start from.
func baseConfig(srv *fakeSMTPServer) Config {
	host, port := srv.addr()
	return Config{Host: host, Port: port, From: "alerts@pulsewatch.invalid"}
}

func testMessage() Message {
	return Message{Subject: "pulsewatch: incident opened", Body: "A monitored target has started failing its checks.\n"}
}

func TestClient_SendDeliversOverPlaintext(t *testing.T) {
	srv := newFakeSMTPServer(t)
	srv.start()
	client, err := NewClient(baseConfig(srv))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if err := client.Send(t.Context(), "ops@example.invalid", testMessage()); err != nil {
		t.Fatalf("Send: %v", err)
	}

	body := srv.sentBody()
	if !strings.Contains(body, "To: ops@example.invalid") {
		t.Fatalf("expected the message to address the real recipient, got:\n%s", body)
	}
	if !strings.Contains(body, "From: alerts@pulsewatch.invalid") {
		t.Fatalf("expected the message to carry the configured From address, got:\n%s", body)
	}
	if !strings.Contains(body, "Subject: pulsewatch: incident opened") {
		t.Fatalf("expected the real subject line, got:\n%s", body)
	}
}

// TestClient_SendRejectsSubjectHeaderInjection proves a Subject containing
// an embedded CRLF is rejected outright — before any network I/O — rather
// than silently mangled into the message: the classic email
// header-injection vulnerability class Client.Send's exported
// Message.Subject would otherwise be a real sink for. No connection to the
// server should even be attempted.
func TestClient_SendRejectsSubjectHeaderInjection(t *testing.T) {
	srv := newFakeSMTPServer(t)
	srv.start()
	client, err := NewClient(baseConfig(srv))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	malicious := Message{
		Subject: "hi\r\nBcc: attacker@evil.invalid\r\nX-Injected: yes",
		Body:    "body",
	}
	err = client.Send(t.Context(), "ops@example.invalid", malicious)
	if err == nil {
		t.Fatal("expected Send to reject a Subject containing CR/LF")
	}
	if KindOf(err) != KindPermanent {
		t.Fatalf("expected KindPermanent for a malformed subject, got %q (%v)", KindOf(err), err)
	}
	if srv.sentBody() != "" {
		t.Fatalf("expected no message to have been sent at all, got:\n%s", srv.sentBody())
	}
}

// TestBuildMessage_SanitizesHeaderValues proves buildMessage's own second,
// redundant layer of CR/LF stripping (belt and braces alongside Send's
// reject, see sanitizeHeaderValue's doc comment) actually works, exercised
// directly since Send's own guard makes this path unreachable through the
// public API for Subject specifically.
func TestBuildMessage_SanitizesHeaderValues(t *testing.T) {
	msg := Message{Subject: "hi\r\nBcc: attacker@evil.invalid", Body: "body"}
	rendered := string(buildMessage("from@example.invalid", "to@example.invalid", msg, time.Now()))
	for _, line := range strings.Split(rendered, "\r\n") {
		if strings.HasPrefix(line, "Bcc:") {
			t.Fatalf("header injection succeeded: found injected header line %q in:\n%s", line, rendered)
		}
	}
	if !strings.Contains(rendered, "Subject: hiBcc: attacker@evil.invalid\r\n") {
		t.Fatalf("expected the CR/LF-stripped subject on one line, got:\n%s", rendered)
	}
}

func TestClient_SendUpgradesToSTARTTLSWhenAdvertised(t *testing.T) {
	srv := newFakeSMTPServer(t)
	srv.offerSTARTTLS = true
	srv.start()
	_, pool := generateTestCert()

	cfg := baseConfig(srv)
	cfg.RootCAs = pool
	client, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if err := client.Send(t.Context(), "ops@example.invalid", testMessage()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if srv.sentBody() == "" {
		t.Fatal("expected a message body to have been received over the upgraded TLS connection")
	}
}

func TestClient_SendUsesImplicitTLS(t *testing.T) {
	srv := newFakeSMTPServer(t)
	srv.implicitTLS = true
	srv.start()
	_, pool := generateTestCert()

	cfg := baseConfig(srv)
	cfg.RootCAs = pool
	cfg.ImplicitTLS = true
	client, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if err := client.Send(t.Context(), "ops@example.invalid", testMessage()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if srv.sentBody() == "" {
		t.Fatal("expected a message body to have been received over the implicit-TLS connection")
	}
}

func TestClient_SendAuthenticatesWhenConfigured(t *testing.T) {
	srv := newFakeSMTPServer(t)
	srv.offerAUTH = true
	var seenUser, seenPass string
	srv.authReply = func(user, pass string) smtpReply {
		seenUser, seenPass = user, pass
		return smtpReply{235, "authentication successful"}
	}
	srv.start()

	cfg := baseConfig(srv)
	cfg.Username = "relay-user"
	cfg.Password = "relay-pass"
	client, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if err := client.Send(t.Context(), "ops@example.invalid", testMessage()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if seenUser != "relay-user" || seenPass != "relay-pass" {
		t.Fatalf("expected the server to observe the configured credentials, got user=%q pass=%q", seenUser, seenPass)
	}
}

func TestClient_SendAuthRejectedIsCredentialKind(t *testing.T) {
	srv := newFakeSMTPServer(t)
	srv.offerAUTH = true
	srv.authReply = func(string, string) smtpReply { return smtpReply{535, "authentication failed"} }
	srv.start()

	cfg := baseConfig(srv)
	cfg.Username = "relay-user"
	cfg.Password = "wrong-pass"
	client, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	err = client.Send(t.Context(), "ops@example.invalid", testMessage())
	if err == nil {
		t.Fatal("expected an error for a rejected AUTH")
	}
	if KindOf(err) != KindCredential {
		t.Fatalf("expected KindCredential for a rejected relay credential, got %q (%v)", KindOf(err), err)
	}
}

func TestClient_SendRecipientRejected550IsPermanentKind(t *testing.T) {
	srv := newFakeSMTPServer(t)
	srv.rcptReply = func(int, string) smtpReply { return smtpReply{550, "mailbox unavailable"} }
	srv.start()

	client, err := NewClient(baseConfig(srv))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	err = client.Send(t.Context(), "bounced@example.invalid", testMessage())
	if err == nil {
		t.Fatal("expected an error for a rejected recipient")
	}
	if KindOf(err) != KindPermanent {
		t.Fatalf("expected KindPermanent for a 550 hard bounce, got %q (%v)", KindOf(err), err)
	}
}

func TestClient_SendRecipientTemporarilyUnavailableIsTransientKind(t *testing.T) {
	srv := newFakeSMTPServer(t)
	srv.rcptReply = func(int, string) smtpReply { return smtpReply{450, "mailbox temporarily unavailable"} }
	srv.start()

	client, err := NewClient(baseConfig(srv))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	err = client.Send(t.Context(), "ops@example.invalid", testMessage())
	if err == nil {
		t.Fatal("expected an error for a temporarily-unavailable recipient")
	}
	if KindOf(err) != KindTransient {
		t.Fatalf("expected KindTransient for a 450 reply, got %q (%v)", KindOf(err), err)
	}
}

func TestClient_SendMailFromRejectedIsPermanentKind(t *testing.T) {
	srv := newFakeSMTPServer(t)
	srv.mailReply = func(int, string) smtpReply { return smtpReply{553, "sender rejected"} }
	srv.start()

	client, err := NewClient(baseConfig(srv))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	err = client.Send(t.Context(), "ops@example.invalid", testMessage())
	if err == nil {
		t.Fatal("expected an error for a rejected sender")
	}
	if KindOf(err) != KindPermanent {
		t.Fatalf("expected KindPermanent for a 553 MAIL FROM rejection, got %q (%v)", KindOf(err), err)
	}
}

func TestClient_SendDataRejectedIsPermanentKind(t *testing.T) {
	srv := newFakeSMTPServer(t)
	srv.dataReply = func(int) smtpReply { return smtpReply{554, "transaction failed"} }
	srv.start()

	client, err := NewClient(baseConfig(srv))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	err = client.Send(t.Context(), "ops@example.invalid", testMessage())
	if err == nil {
		t.Fatal("expected an error for a rejected DATA command")
	}
	if KindOf(err) != KindPermanent {
		t.Fatalf("expected KindPermanent for a 554 DATA rejection, got %q (%v)", KindOf(err), err)
	}
}

func TestClient_SendDialFailureIsTransientKind(t *testing.T) {
	// A listener that is opened then immediately closed frees its port back
	// to the OS in a state real code must handle exactly like any other
	// down relay: nothing is listening there, so the dial itself fails.
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	tcpAddr := ln.Addr().(*net.TCPAddr)
	_ = ln.Close()

	cfg := Config{Host: "127.0.0.1", Port: tcpAddr.Port, From: "alerts@pulsewatch.invalid"}
	client, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	err = client.Send(ctx, "ops@example.invalid", testMessage())
	if err == nil {
		t.Fatal("expected an error dialing a closed port")
	}
	if KindOf(err) != KindTransient {
		t.Fatalf("expected KindTransient for a dial failure, got %q (%v)", KindOf(err), err)
	}
}

// TestClassify_AuthPhaseNonProtocolErrorIsCredential proves the one AUTH
// failure shape a real server can never produce (net/smtp's own PlainAuth
// refusing to send credentials over a connection it doesn't consider
// trusted, before any SMTP command is even sent) is still classified as
// "our own configuration", not silently treated as retryable.
func TestClassify_AuthPhaseNonProtocolErrorIsCredential(t *testing.T) {
	err := classify(phaseAuth, errors.New("unencrypted connection"))
	if err.Kind != KindCredential {
		t.Fatalf("expected KindCredential, got %q", err.Kind)
	}
}

func TestClassify_ConnectPhaseTextprotoErrorIsClassifiedByCode(t *testing.T) {
	transient := classify(phaseConnect, &textproto.Error{Code: 421, Msg: "service not available"})
	if transient.Kind != KindTransient {
		t.Fatalf("expected a 421 greeting rejection to be transient, got %q", transient.Kind)
	}
	permanent := classify(phaseConnect, &textproto.Error{Code: 554, Msg: "no SMTP service here"})
	if permanent.Kind != KindPermanent {
		t.Fatalf("expected a 554 greeting rejection to be permanent, got %q", permanent.Kind)
	}
}

func TestNewClient_RejectsIncompleteConfig(t *testing.T) {
	if _, err := NewClient(Config{}); err == nil {
		t.Fatal("expected an error for a config with no Host/From")
	}
	if _, err := NewClient(Config{Host: "smtp.example.invalid", From: "a@example.invalid", Username: "u"}); err == nil {
		t.Fatal("expected an error for a Username with no Password")
	}
}

func TestConfigFromEnv(t *testing.T) {
	t.Run("unset host is a plain error", func(t *testing.T) {
		t.Setenv("SMTP_HOST", "")
		t.Setenv("SMTP_FROM_ADDRESS", "")
		if _, err := ConfigFromEnv(); err == nil {
			t.Fatal("expected an error when SMTP_HOST/SMTP_FROM_ADDRESS are unset")
		}
	})

	t.Run("a complete config loads with real defaults", func(t *testing.T) {
		t.Setenv("SMTP_HOST", "smtp.example.invalid")
		t.Setenv("SMTP_FROM_ADDRESS", "alerts@example.invalid")
		t.Setenv("SMTP_PORT", "")
		t.Setenv("SMTP_USERNAME", "")
		t.Setenv("SMTP_PASSWORD", "")
		t.Setenv("SMTP_IMPLICIT_TLS", "")

		cfg, err := ConfigFromEnv()
		if err != nil {
			t.Fatalf("ConfigFromEnv: %v", err)
		}
		if cfg.Port != defaultPort {
			t.Fatalf("expected the default port %d, got %d", defaultPort, cfg.Port)
		}
		if cfg.ImplicitTLS {
			t.Fatal("expected ImplicitTLS to default to false")
		}
	})

	t.Run("a malformed port is a plain error, not a silent fallback", func(t *testing.T) {
		t.Setenv("SMTP_HOST", "smtp.example.invalid")
		t.Setenv("SMTP_FROM_ADDRESS", "alerts@example.invalid")
		t.Setenv("SMTP_PORT", "not-a-port")
		if _, err := ConfigFromEnv(); err == nil {
			t.Fatal("expected an error for a malformed SMTP_PORT")
		}
	})
}
