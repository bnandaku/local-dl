package main

import (
	"net/http"
	"testing"
)

func TestBotAuthorizationDoesNotLeakToOtherHosts(t *testing.T) {
	t.Setenv("BOT_SERVICE_TOKEN", "private")
	old := RemoteServer
	RemoteServer = "http://bot.internal:8080"
	defer func() { RemoteServer = old }()
	good, _ := http.NewRequest("POST", RemoteServer+"/queue", nil)
	authorizeBotRequest(good)
	if good.Header.Get("Authorization") != "Bearer private" {
		t.Fatal("missing auth")
	}
	bad, _ := http.NewRequest("GET", "https://download.example/file", nil)
	authorizeBotRequest(bad)
	if bad.Header.Get("Authorization") != "" {
		t.Fatal("leaked service token")
	}
}
