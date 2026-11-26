package ownerweb

import (
	"context"
	"net/http"
	"os"
	"strings"
	"time"

	"mcpshka/internal/lanelink"
)

func (p *connPortal) api(w http.ResponseWriter, r *http.Request) {
	if !p.authed(r) {
		jsonErr(w, "нужен вход через Face ID", http.StatusUnauthorized)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	action := strings.TrimPrefix(r.URL.Path, "/connections/api/")
	if action == "issues" || strings.HasPrefix(action, "issues/") {
		if IssuesAPI == nil {
			http.NotFound(w, r)
			return
		}
		IssuesAPI(ctx, w, r, strings.TrimPrefix(strings.TrimPrefix(action, "issues"), "/"))
		return
	}
	if action == "memory" || strings.HasPrefix(action, "memory/") {
		memoryAPI(ctx, w, r, strings.TrimPrefix(strings.TrimPrefix(action, "memory"), "/"))
		return
	}
	if action == "tool" || strings.HasPrefix(action, "code-") {
		p.toolsAPI(ctx, w, r, action)
		return
	}
	if action == "runners" || strings.HasPrefix(action, "runner-") {
		p.runnersAPI(ctx, w, r, action)
		return
	}
	if strings.HasPrefix(action, "live-") {
		p.liveAPI(w, r, action)
		return
	}
	if action == "plugins" || strings.HasPrefix(action, "plugin-") {
		p.pluginsAPI(ctx, w, r, action)
		return
	}
	if action == "cleanup-plan" || action == "cleanup-apply" {
		p.cleanupAPI(ctx, w, r, action)
		return
	}
	if action == "containers" || action == "container-act" || action == "container-logs" {
		p.containersAPI(ctx, w, r, action)
		return
	}
	if action == "catalog" && r.Method == http.MethodGet {
		JSON(w, map[string]any{"connections": p.statuses(ctx)}, http.StatusOK)
		return
	}
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	var in struct {
		ID, Token, ClientID, ClientSecret, Landed string
		Fields                                    map[string]string
		Enabled                                   bool
		nodeRequest
	}
	if err := ReadBody(r, &in); err != nil {
		jsonErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	if strings.HasPrefix(action, "node-") {
		nodeAPI(ctx, w, action, in.nodeRequest)
		return
	}
	if action == "brain" {
		if err := setBrain(in.ID, in.Enabled); err != nil {
			jsonErr(w, err.Error(), http.StatusBadRequest)
			return
		}
		JSON(w, map[string]any{"ok": true}, http.StatusOK)
		return
	}
	c, ok := connFind(in.ID)
	if !ok {
		jsonErr(w, "неизвестное подключение", http.StatusBadRequest)
		return
	}
	fail := func(msg string, code int) { jsonErr(w, msg, code) }
	switch action {
	case "token":
		if c.Kind != "token" {
			fail("это подключение не принимает токен", http.StatusBadRequest)
			return
		}
		token := strings.TrimSpace(in.Token)
		if err := validConnToken(token); err != nil {
			fail(err.Error(), http.StatusBadRequest)
			return
		}
		ch := p.check(ctx, c, token)
		if !ch.OK {
			fail("не сохранён: "+ch.Detail, http.StatusUnprocessableEntity)
			return
		}
		if err := connWriteToken(c.Path, token); err != nil {
			fail("проверен, но не сохранён: "+err.Error(), http.StatusInternalServerError)
			return
		}
		JSON(w, map[string]any{"ok": true, "check": ch, "last4": last4(token)}, http.StatusOK)
	case "check":
		if c.Kind != "token" {
			fail("проверка доступна только для токенов", http.StatusBadRequest)
			return
		}
		t, _, has := connReadToken(c.Path)
		if !has {
			fail("токен ещё не сохранён", http.StatusNotFound)
			return
		}
		JSON(w, map[string]any{"ok": true, "check": p.check(ctx, c, t)}, http.StatusOK)
	case "cloud-app":
		if c.Kind != "cloud" {
			fail("это не облако", http.StatusBadRequest)
			return
		}
		if err := SaveStorageApp(c.ID, StorageOAuthApp{ClientID: strings.TrimSpace(in.ClientID), ClientSecret: strings.TrimSpace(in.ClientSecret)}); err != nil {
			fail(err.Error(), http.StatusBadRequest)
			return
		}
		JSON(w, map[string]bool{"ok": true}, http.StatusOK)
	case "cloud-connect":
		if c.Kind != "cloud" {
			fail("это не облако", http.StatusBadRequest)
			return
		}
		link, _, err := p.storage.begin(c.ID, connStorageOwner)
		if err != nil {
			fail(err.Error(), http.StatusBadRequest)
			return
		}
		JSON(w, map[string]string{"url": link}, http.StatusOK)
	case "lane-pair", "lane-rotate", "lane-remove":
		if c.Kind != "lane" {
			fail("это не MCP-порт", http.StatusBadRequest)
			return
		}
		l := p.lane()
		var err error
		switch action {
		case "lane-pair":
			host, _ := os.Hostname()
			var st lanelink.Status
			if st, err = l.Pair(ctx, "MCPshka "+host, laneLocalURL); err == nil {
				JSON(w, st, http.StatusOK)
				return
			}
		case "lane-rotate":
			err = l.Rotate(ctx)
		default:
			err = l.Remove(ctx)
		}
		if err != nil {
			fail(err.Error(), http.StatusBadGateway)
			return
		}
		JSON(w, map[string]bool{"ok": true}, http.StatusOK)
	case "ci-secret":
		if c.ID != "moshub" {
			fail("секрет CI есть только у MosHub", http.StatusBadRequest)
			return
		}
		if err := pushMosHubSecret(ctx, p.http); err != nil {
			fail(err.Error(), http.StatusBadGateway)
			return
		}
		JSON(w, map[string]bool{"ok": true}, http.StatusOK)
	case "form", "form-check":
		f, isForm := connForms[c.ID]
		if c.Kind != "form" || !isForm {
			fail("это подключение не форма", http.StatusBadRequest)
			return
		}
		v, _ := readForm(c.ID)
		if action == "form" {
			var err error
			if v, err = formValues(c.ID, in.Fields); err != nil {
				fail(err.Error(), http.StatusBadRequest)
				return
			}
		} else if v == nil {
			fail("ещё не сохранено", http.StatusNotFound)
			return
		}
		ch := f.check(ctx, p.http, v)
		if len(ch.Detail) > 300 {
			ch.Detail = ch.Detail[:300]
		}
		p.mu.Lock()
		p.checks[c.ID] = ch
		p.mu.Unlock()
		if !ch.OK {
			fail("не сохранено: "+ch.Detail, http.StatusUnprocessableEntity)
			return
		}
		if action == "form" {
			if err := writeForm(c.ID, v); err != nil {
				fail("проверено, но не сохранено: "+err.Error(), http.StatusInternalServerError)
				return
			}
		}
		JSON(w, map[string]any{"ok": true, "check": ch}, http.StatusOK)
	case "cloud-start":
		if c.Kind != "cloud" {
			fail("это не облако", http.StatusBadRequest)
			return
		}
		if !storageQuick[c.ID] {
			fail("для этого облака вход через rclone не работает — нужен мастер «Подключить»", http.StatusBadRequest)
			return
		}
		link, err := cloudWeb.begin(c.ID)
		if err != nil {
			fail(err.Error(), http.StatusBadGateway)
			return
		}
		JSON(w, map[string]string{"url": link}, http.StatusOK)
	case "cloud-finish":
		if c.Kind != "cloud" {
			fail("это не облако", http.StatusBadRequest)
			return
		}
		token, err := cloudWeb.finish(c.ID, in.Landed)
		if err == nil && c.ID == "onedrive" {
			token, err = oneDriveDrive(ctx, p.http, token)
		}
		if err == nil {
			err = storageWeb.accept(ctx, c.ID, connStorageOwner, token)
		}
		if err != nil {
			fail(err.Error(), http.StatusUnprocessableEntity)
			return
		}
		JSON(w, map[string]bool{"ok": true}, http.StatusOK)
	case "agent-login", "agent-status":
		if c.ID != "codex" {
			fail("вход поддержан только для Codex", http.StatusBadRequest)
			return
		}
		verb := "codex-status"
		if action == "agent-login" {
			verb = "codex-begin"
		}
		raw, err := p.broker(ctx, "agent-login", verb)
		if err != nil {
			fail(err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
	default:
		http.NotFound(w, r)
	}
}
