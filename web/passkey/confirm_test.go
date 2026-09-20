package passkey

import "testing"

func TestConfirmGrantRoundTrip(t *testing.T) {
	resetConfirmGrantsForTest()
	id := PutConfirmGrant("user-a")
	grant, err := TakeConfirmGrant(id)
	if err != nil {
		t.Fatalf("TakeConfirmGrant: %v", err)
	}
	if grant.UserUUID != "user-a" {
		t.Fatalf("user = %s", grant.UserUUID)
	}
	if _, err := TakeConfirmGrant(id); err == nil {
		t.Fatal("expected one-time confirm grant")
	}
}

func TestConfirmGrantUnknown(t *testing.T) {
	resetConfirmGrantsForTest()
	if _, err := TakeConfirmGrant("missing"); err == nil {
		t.Fatal("expected missing grant to fail")
	}
}

func TestFinishUserAssertionRejectsEmpty(t *testing.T) {
	if err := FinishUserAssertion(nil, "", "", nil); err == nil {
		t.Fatal("expected empty assertion to fail")
	}
}
