package main

import (
	"net/smtp"
	"strings"
	"testing"
)

func TestLoginAuthExchange(t *testing.T) {
	a := &loginAuth{user: "someone@example.com", pass: "token", host: "smtp.example.com"}
	// credentials must never go out in the clear to a remote server
	if _, _, err := a.Start(&smtp.ServerInfo{Name: "smtp.example.com", TLS: false}); err == nil {
		t.Fatal("LOGIN accepted an unencrypted connection")
	}
	proto, _, err := a.Start(&smtp.ServerInfo{Name: "smtp.example.com", TLS: true})
	if err != nil || proto != "LOGIN" {
		t.Fatalf("Start over TLS: %q %v", proto, err)
	}
	user, err := a.Next([]byte("Username:"), true)
	if err != nil || string(user) != "someone@example.com" {
		t.Fatalf("username step: %q %v", user, err)
	}
	pass, err := a.Next([]byte("Password:"), true)
	if err != nil || string(pass) != "token" {
		t.Fatalf("password step: %q %v", pass, err)
	}
	if _, err := a.Next([]byte("Surprise:"), true); err == nil {
		t.Fatal("unknown challenge accepted")
	}
}

func TestValidEmail(t *testing.T) {
	for _, ok := range []string{"a@b.co", "augusto@augustogunsch.com", "x.y+z@mail.example.org"} {
		if !validEmail(ok) {
			t.Errorf("rejected valid address %q", ok)
		}
	}
	for _, bad := range []string{"", "nope", "a@b", "a@b.co\r\nBcc: evil@x.com", "<a@b.co>", strings.Repeat("a", 250) + "@b.co"} {
		if validEmail(bad) {
			t.Errorf("accepted invalid address %q", bad)
		}
	}
}
