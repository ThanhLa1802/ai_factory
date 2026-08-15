package auth

import (
	"testing"
	"time"
)

func TestIssueAndParse(t *testing.T) {
	secret := []byte("0123456789abcdef")
	token, err := IssueToken(secret, "u1", "t1", RoleTenantAdmin, time.Hour)
	if err != nil {
		t.Fatalf("IssueToken error = %v", err)
	}
	claims, err := ParseToken(secret, token)
	if err != nil {
		t.Fatalf("ParseToken error = %v", err)
	}
	if claims.UserID != "u1" || claims.TenantID != "t1" || claims.Role != RoleTenantAdmin {
		t.Errorf("claims = %+v", claims)
	}
}

func TestParseRejectsWrongSecret(t *testing.T) {
	token, _ := IssueToken([]byte("0123456789abcdef"), "u1", "t1", RoleTenantViewer, time.Hour)
	if _, err := ParseToken([]byte("fedcba9876543210"), token); err == nil {
		t.Error("expected error for wrong secret")
	}
}
