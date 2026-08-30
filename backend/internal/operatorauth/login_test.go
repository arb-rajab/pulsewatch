package operatorauth

import "testing"

func TestLogin_RealCredentialRoundTrip(t *testing.T) {
	pool := testPool(t)
	operatorID := insertTestOperator(t, pool, "test-login-ok@example.invalid", "a-real-password")

	identity, err := Login(t.Context(), pool, "test-login-ok@example.invalid", "a-real-password")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if identity.OperatorID != operatorID {
		t.Fatalf("expected operator id %s, got %s", operatorID, identity.OperatorID)
	}
}

func TestLogin_WrongPasswordRejected(t *testing.T) {
	pool := testPool(t)
	insertTestOperator(t, pool, "test-login-wrongpw@example.invalid", "a-real-password")

	if _, err := Login(t.Context(), pool, "test-login-wrongpw@example.invalid", "not-the-password"); err != ErrInvalidCredential {
		t.Fatalf("expected ErrInvalidCredential, got %v", err)
	}
}

func TestLogin_UnknownEmailRejected(t *testing.T) {
	pool := testPool(t)
	if _, err := Login(t.Context(), pool, "no-such-operator@example.invalid", "anything"); err != ErrInvalidCredential {
		t.Fatalf("expected ErrInvalidCredential for an unknown email, got %v", err)
	}
}

func TestLogin_UnknownEmailAndWrongPasswordReturnTheIdenticalError(t *testing.T) {
	pool := testPool(t)
	insertTestOperator(t, pool, "test-login-identical-err@example.invalid", "a-real-password")

	_, unknownErr := Login(t.Context(), pool, "definitely-not-registered@example.invalid", "anything")
	_, wrongPwErr := Login(t.Context(), pool, "test-login-identical-err@example.invalid", "wrong")

	if unknownErr != ErrInvalidCredential || wrongPwErr != ErrInvalidCredential {
		t.Fatalf("expected both failure modes to return the identical ErrInvalidCredential (no account-enumeration signal), got %v and %v", unknownErr, wrongPwErr)
	}
}
