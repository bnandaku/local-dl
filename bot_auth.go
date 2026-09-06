package main

import (
	"net/http"
	"net/url"
	"os"
)

func authorizeBotRequest(req *http.Request) {
	base, err := url.Parse(RemoteServer)
	if err != nil || req == nil || req.URL == nil {
		return
	}
	if req.URL.Scheme != base.Scheme || req.URL.Host != base.Host {
		return
	}
	if token := os.Getenv("BOT_SERVICE_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
}
