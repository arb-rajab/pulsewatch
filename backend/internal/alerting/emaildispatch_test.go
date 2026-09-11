package alerting

import (
	"bytes"
	"log/slog"
	"net"
	"net/textproto"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arb-rajab/pulsewatch/backend/internal/emailprovider"
)

// These tests exercise the real EmailDispatcher against a real Postgres and
// a fake server speaking the real SMTP wire protocol — the same shape
// Session 17/18's webhook/push tests took (real listener, real database,
// real retry timing scaled down to milliseconds).

// smtpReply is one scripted server response to a command.
type smtpReply struct {
	code int
	msg  string
}

// fakeSMTPServer is a minimal, real SMTP server for end-to-end
// NotifyChannels tests: a real net.Listener and a real line-oriented
// net/textproto exchange, not a mocked EmailDispatcher.Dispatch. It is a
// smaller, independent server from internal/emailprovider's own protocol-
// level fakeSMTPServer — the same "each package builds its own test double
// at its own level" split internal/pushprovider's fcmServer and this
// package's mockFCM already establish for push.
//
// alert_channels is a global table with best-effort (not FK-cascading)
// cleanup, exactly as ADR-0006's Consequences and B-006 already document —
// once a channel has an alert_dispatches row (every test here produces
// one), its own t.Cleanup delete routinely no-ops, so a channel some
// earlier test in this file created can still be live when a later test's
// NotifyChannels call loads every alert_channels row. Unlike webhook/push
// (where each channel's destination directly names a specific, per-test
// server), an email channel's destination is only ever the recipient
// address — the relay to dial is this dispatcher's own Config, shared by
// every channel NotifyChannels hands it. So a leftover channel from an
// earlier test really does reach *this* test's server too. Tracking
// attempts per recipient address (not a single server-wide counter) is
// what keeps this file's own assertions correct despite that, without
// touching B-006 itself (explicitly out of this session's scope).
type fakeSMTPServer struct {
	ln net.Listener

	// rcptReply decides each RCPT TO reply; attempt is 1-indexed per
	// distinct recipient address this server has seen (a fresh connection
	// per EmailDispatcher retry, matching production's own per-attempt
	// dial). nil means always accept.
	rcptReply func(attempt int, to string) smtpReply

	mu                sync.Mutex
	recipientAttempts map[string]int
}

func newFakeSMTPServer(t *testing.T) *fakeSMTPServer {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &fakeSMTPServer{ln: ln, recipientAttempts: make(map[string]int)}
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

// attemptsFor reports how many RCPT TO attempts this server has seen for a
// specific recipient address — scoped, unlike a raw connection count, so it
// stays correct even when a leftover channel from an earlier test (see the
// type doc above) also dispatches through this same server.
func (s *fakeSMTPServer) attemptsFor(to string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.recipientAttempts[to]
}

func (s *fakeSMTPServer) start() {
	go func() {
		for {
			conn, err := s.ln.Accept()
			if err != nil {
				return
			}
			go s.handleConn(conn)
		}
	}()
}

func (s *fakeSMTPServer) hostPort() (string, int) {
	addr := s.ln.Addr().(*net.TCPAddr)
	return "127.0.0.1", addr.Port
}

func (s *fakeSMTPServer) handleConn(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	tp := textproto.NewConn(conn)
	_ = tp.PrintfLine("220 fake.test ESMTP ready")

	for {
		line, err := tp.ReadLine()
		if err != nil {
			return
		}
		upper := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
			_ = tp.PrintfLine("250 fake.test")
		case strings.HasPrefix(upper, "MAIL FROM:"):
			_ = tp.PrintfLine("250 OK")
		case strings.HasPrefix(upper, "RCPT TO:"):
			to := strings.TrimSuffix(strings.TrimPrefix(line[len("RCPT TO:"):], "<"), ">")
			s.mu.Lock()
			s.recipientAttempts[to]++
			attempt := s.recipientAttempts[to]
			s.mu.Unlock()

			reply := smtpReply{250, "OK"}
			if s.rcptReply != nil {
				reply = s.rcptReply(attempt, to)
			}
			_ = tp.PrintfLine("%d %s", reply.code, reply.msg)
		case strings.HasPrefix(upper, "DATA"):
			_ = tp.PrintfLine("354 go ahead")
			var buf bytes.Buffer
			_, _ = buf.ReadFrom(tp.DotReader())
			_ = tp.PrintfLine("250 message accepted")
		case strings.HasPrefix(upper, "QUIT"):
			_ = tp.PrintfLine("221 bye")
			return
		default:
			_ = tp.PrintfLine("500 unrecognized command")
		}
	}
}

// fastEmailDispatcher is the real EmailDispatcher with backoff scaled to
// milliseconds — real retry logic, no real wall-clock seconds.
func fastEmailDispatcher(srv *fakeSMTPServer) *EmailDispatcher {
	host, port := srv.hostPort()
	return &EmailDispatcher{
		Config:            emailprovider.Config{Host: host, Port: port, From: "alerts@pulsewatch.invalid"},
		MaxAttempts:       3,
		BackoffBase:       5 * time.Millisecond,
		BackoffMax:        20 * time.Millisecond,
		PerAttemptTimeout: 2 * time.Second,
	}
}

// TestNotifyChannels_EmailDeliversAndRecordsDispatch is the end-to-end
// success proof: a real email channel (destination is a plain recipient
// address, decrypted by LoadChannels exactly like a webhook URl), a real
// SMTP exchange against a real listener, and delivery_confirmed=true
// recorded from a real 250 on every protocol step — never a log write
// standing in for one.
func TestNotifyChannels_EmailDeliversAndRecordsDispatch(t *testing.T) {
	pool := testPool(t)
	targetID := insertTestTargetRow(t, pool)

	srv := newFakeSMTPServer(t)
	srv.start()

	recipient := "ops-" + randomSuffix(t) + "@example.invalid"
	channelID := insertTestAlertChannel(t, pool, "email", recipient)

	req, err := OpenIncident(t.Context(), pool, targetID)
	if err != nil || req == nil {
		t.Fatalf("OpenIncident (setup): %v (req=%v)", err, req)
	}

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	NotifyChannels(t.Context(), pool, fastEmailDispatcher(srv), testEncryptionKey, *req, logger)

	confirmed, attempts, lastError := fetchDispatchRow(t, pool, req.IncidentID, channelID)
	if !confirmed {
		t.Fatalf("expected delivery_confirmed=true: the fake relay accepted every step (last_error=%q)", lastError)
	}
	if attempts != 1 {
		t.Fatalf("expected attempts=1 for an immediate success, got %d", attempts)
	}
	if lastError != "" {
		t.Fatalf("expected no last_error on a confirmed delivery, got %q", lastError)
	}
	if strings.Contains(logBuf.String(), recipient) {
		t.Fatalf("FR-023 violation: recipient address appeared in dispatch logs:\n%s", logBuf.String())
	}
}

// TestEmailDispatcher_RecipientRejectedIsNeverRetried proves B-014's central
// retry-policy decision: a hard bounce (RCPT TO rejected 5xx, the SMTP-
// protocol shape of an invalid/unknown address) is recorded unconfirmed
// after exactly one attempt — never retried, the same "no blind retry"
// ADR-0007 established for push's dead-token case, applied here to SMTP's
// own failure vocabulary instead of a device-token table this single-
// destination channel has no need for.
func TestEmailDispatcher_RecipientRejectedIsNeverRetried(t *testing.T) {
	pool := testPool(t)
	targetID := insertTestTargetRow(t, pool)

	srv := newFakeSMTPServer(t)
	recipient := "bounced-" + randomSuffix(t) + "@example.invalid"
	srv.rcptReply = func(_ int, to string) smtpReply {
		if to == recipient {
			return smtpReply{550, "mailbox unavailable"}
		}
		return smtpReply{250, "OK"}
	}
	srv.start()

	channelID := insertTestAlertChannel(t, pool, "email", recipient)

	req, err := OpenIncident(t.Context(), pool, targetID)
	if err != nil || req == nil {
		t.Fatalf("OpenIncident (setup): %v (req=%v)", err, req)
	}

	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	NotifyChannels(t.Context(), pool, fastEmailDispatcher(srv), testEncryptionKey, *req, logger)

	if got := srv.attemptsFor(recipient); got != 1 {
		t.Fatalf("expected exactly 1 SMTP RCPT attempt for a hard bounce, got %d", got)
	}

	confirmed, attempts, lastError := fetchDispatchRow(t, pool, req.IncidentID, channelID)
	if confirmed {
		t.Fatal("expected delivery_confirmed=false: the recipient was rejected with a permanent 550")
	}
	if attempts != 1 {
		t.Fatalf("expected attempts=1 (a hard bounce is never retried), got %d", attempts)
	}
	if !strings.Contains(lastError, "rejected") {
		t.Fatalf("expected last_error to describe the rejection, got %q", lastError)
	}
	if strings.Contains(lastError, recipient) {
		t.Fatalf("FR-023 violation: recipient address leaked into last_error: %q", lastError)
	}
}

// TestEmailDispatcher_RetriesTransientFailureThenSucceeds proves the actual
// retry/backoff behavior for a transient SMTP failure (a 4xx RCPT TO reply —
// "mailbox temporarily unavailable", the closest SMTP-protocol sibling of a
// webhook's 5xx/429): a channel whose relay rejects the first two attempts
// then accepts the third is recorded confirmed, with attempts=3 — the retry
// genuinely happened, driven through NotifyChannels end to end.
func TestEmailDispatcher_RetriesTransientFailureThenSucceeds(t *testing.T) {
	pool := testPool(t)
	targetID := insertTestTargetRow(t, pool)

	srv := newFakeSMTPServer(t)
	recipient := "ops-" + randomSuffix(t) + "@example.invalid"
	srv.rcptReply = func(attempt int, to string) smtpReply {
		if to != recipient {
			return smtpReply{250, "OK"}
		}
		if attempt < 3 {
			return smtpReply{450, "mailbox temporarily unavailable"}
		}
		return smtpReply{250, "OK"}
	}
	srv.start()

	channelID := insertTestAlertChannel(t, pool, "email", recipient)

	req, err := OpenIncident(t.Context(), pool, targetID)
	if err != nil || req == nil {
		t.Fatalf("OpenIncident (setup): %v (req=%v)", err, req)
	}

	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	NotifyChannels(t.Context(), pool, fastEmailDispatcher(srv), testEncryptionKey, *req, logger)

	if got := srv.attemptsFor(recipient); got != 3 {
		t.Fatalf("expected exactly 3 SMTP RCPT attempts for this recipient, got %d", got)
	}

	confirmed, attempts, lastError := fetchDispatchRow(t, pool, req.IncidentID, channelID)
	if !confirmed {
		t.Fatalf("expected delivery_confirmed=true after the 3rd attempt succeeded (last_error=%q)", lastError)
	}
	if attempts != 3 {
		t.Fatalf("expected attempts=3 (2 failures then a success), got %d", attempts)
	}
}

// TestEmailDispatcher_ExhaustedRetriesRecordUnconfirmed proves the give-up
// path: a relay that always returns a transient failure exhausts every
// retry, alert_dispatches ends up with delivery_confirmed=false, attempts
// equal to the configured max, and a non-empty last_error.
func TestEmailDispatcher_ExhaustedRetriesRecordUnconfirmed(t *testing.T) {
	pool := testPool(t)
	targetID := insertTestTargetRow(t, pool)

	srv := newFakeSMTPServer(t)
	srv.rcptReply = func(int, string) smtpReply { return smtpReply{421, "service not available"} }
	srv.start()

	channelID := insertTestAlertChannel(t, pool, "email", "ops-"+randomSuffix(t)+"@example.invalid")

	req, err := OpenIncident(t.Context(), pool, targetID)
	if err != nil || req == nil {
		t.Fatalf("OpenIncident (setup): %v (req=%v)", err, req)
	}

	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	dispatcher := fastEmailDispatcher(srv)
	NotifyChannels(t.Context(), pool, dispatcher, testEncryptionKey, *req, logger)

	confirmed, attempts, lastError := fetchDispatchRow(t, pool, req.IncidentID, channelID)
	if confirmed {
		t.Fatal("expected delivery_confirmed=false: every attempt returned a transient 421")
	}
	if attempts != dispatcher.MaxAttempts {
		t.Fatalf("expected attempts=%d (every configured attempt exhausted), got %d", dispatcher.MaxAttempts, attempts)
	}
	if lastError == "" {
		t.Fatal("expected a non-empty last_error describing the failure")
	}
}

// TestEmailDispatcher_UnconfiguredRelayIsHonestlyUnconfirmed proves the
// fail-safe for a fresh install with no SMTP_HOST/SMTP_FROM_ADDRESS
// configured: an email channel is reported unconfirmed with a clear reason,
// the same fail-safe PushDispatcher's nil Pool already establishes, rather
// than a panic in the scheduler's hot path or (ADR-0006's original worry)
// a silently dropped notification.
func TestEmailDispatcher_UnconfiguredRelayIsHonestlyUnconfirmed(t *testing.T) {
	pool := testPool(t)
	targetID := insertTestTargetRow(t, pool)
	channelID := insertTestAlertChannel(t, pool, "email", "ops-"+randomSuffix(t)+"@example.invalid")

	req, err := OpenIncident(t.Context(), pool, targetID)
	if err != nil || req == nil {
		t.Fatalf("OpenIncident (setup): %v (req=%v)", err, req)
	}

	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	dispatcher := &EmailDispatcher{} // zero Config: no SMTP_HOST/SMTP_FROM_ADDRESS
	NotifyChannels(t.Context(), pool, dispatcher, testEncryptionKey, *req, logger)

	confirmed, attempts, lastError := fetchDispatchRow(t, pool, req.IncidentID, channelID)
	if confirmed {
		t.Fatal("expected delivery_confirmed=false: no SMTP relay is configured")
	}
	if attempts != 0 {
		t.Fatalf("expected attempts=0 (no relay to even dial), got %d", attempts)
	}
	if lastError == "" {
		t.Fatal("expected a non-empty last_error explaining the relay is not configured")
	}
}
