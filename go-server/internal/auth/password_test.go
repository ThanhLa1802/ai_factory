package auth

import "testing"

func TestHashAndVerify(t *testing.T) {
	hash, err := HashPassword("hunter2")
	if err != nil {
		t.Fatalf("HashPassword error = %v", err)
	}
	if hash == "hunter2" {
		t.Fatal("hash must not be plaintext")
	}
	if !VerifyPassword(hash, "hunter2") {
		t.Error("VerifyPassword(correct) = false, want true")
	}
	if VerifyPassword(hash, "wrong") {
		t.Error("VerifyPassword(wrong) = true, want false")
	}
}
