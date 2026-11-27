package ownerweb

import (
	"context"
	"net/http"
	"strings"
	"time"

	"mcpshka/internal/nodes"
)

const nodesGroup = "Узлы (VPS и раннеры)"

// nodeStatuses lists SSH nodes without touching the network: a page load
// must not wait for a VPS. The host key is compared on «Сверить ключ».
func nodeStatuses() []ConnStatus {
	list, err := nodes.List()
	out := []ConnStatus{{ID: "node-new", Group: nodesGroup, Title: "Добавить узел", Kind: "node-add", State: "add",
		Why: "IP, порт и пользователь. MCPshka создаст свой ключ — добавьте его строку в ~/.ssh/authorized_keys на узле, затем сверьте отпечаток ключа хоста с консолью провайдера и примите."}}
	if err != nil {
		out[0].Hint = "каталог ключей недоступен: " + err.Error()
		return out
	}
	for _, n := range list {
		n := n
		s := ConnStatus{ID: "node-" + n.Name, Group: nodesGroup, Title: n.Name, Kind: "node", Node: &n, State: "connected",
			Why: "Ключ MCPshka и закреплённый ключ хоста."}
		if n.Addr == nil {
			s.State, s.Why = "unknown", "Адрес не записан: "+n.AddressNote
		} else {
			s.Why = n.Addr.User + "@" + n.Addr.Host
			if n.Addr.Port != 22 {
				s.Why += " порт " + itoa(n.Addr.Port)
			}
		}
		switch {
		case len(n.Pinned) == 0:
			s.State = "pin"
		case s.State != "connected":
		case n.LastCheck == nil:
			s.State = "saved"
		case n.LastCheck.OK && time.Since(n.LastCheck.At) > 10*time.Minute:
			s.State = "saved"
			s.Hint = "проверка SSH устарела — проверьте связь снова"
		case !n.LastCheck.OK:
			s.State = "offline"
			s.Hint = "последняя проверка " + n.LastCheck.At.Format("02.01 15:04") + " UTC: " + n.LastCheck.Error
			if strings.Contains(n.LastCheck.Error, "Host key") || strings.Contains(n.LastCheck.Error, "HOST IDENTIFICATION") {
				s.Hint = "ключ хоста сменился (переустановка VPS или подмена): «Сверить ключ», сравнить отпечаток с консолью провайдера, только потом принять"
			}
		}
		out = append(out, s)
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

type nodeRequest struct {
	Name, Host, User, Fingerprint string
	PeerIP, PeerKey               string
	Port                          int
}

// nodeAPI serves /connections/api/node-*; the caller already passed Face ID.
func nodeAPI(ctx context.Context, w http.ResponseWriter, action string, in nodeRequest) {
	fail := func(err error) { jsonErr(w, err.Error(), http.StatusUnprocessableEntity) }
	name := strings.TrimPrefix(strings.TrimSpace(in.Name), "node-")
	switch action {
	case "node-add":
		if in.Port == 0 {
			in.Port = 22
		}
		n, err := nodes.Add(ctx, name, nodes.Addr{Host: strings.TrimSpace(in.Host), Port: in.Port, User: strings.TrimSpace(in.User)})
		if err != nil {
			fail(err)
			return
		}
		JSON(w, map[string]any{"ok": true, "node": n}, http.StatusOK)
	case "node-scan":
		hk, _, err := nodes.Scan(ctx, name)
		if err != nil {
			fail(err)
			return
		}
		JSON(w, map[string]any{"ok": true, "host_key": hk}, http.StatusOK)
	case "node-accept":
		hk, err := nodes.Accept(ctx, name, strings.TrimSpace(in.Fingerprint))
		if err != nil {
			fail(err)
			return
		}
		JSON(w, map[string]any{"ok": true, "host_key": hk}, http.StatusOK)
	case "node-check":
		if err := nodes.Check(ctx, name); err != nil {
			fail(err)
			return
		}
		JSON(w, map[string]any{"ok": true}, http.StatusOK)
	case "node-inspect":
		profile, err := nodes.Inspect(ctx, name)
		if err != nil {
			fail(err)
			return
		}
		JSON(w, map[string]any{"ok": true, "inspection": profile}, http.StatusOK)
	case "node-mesh":
		peer, err := nodes.ObserveMesh(ctx, name, strings.TrimSpace(in.PeerIP), strings.TrimSpace(in.PeerKey))
		if err != nil {
			fail(err)
			return
		}
		JSON(w, map[string]any{"ok": true, "mesh": peer}, http.StatusOK)
	case "node-remove":
		if err := nodes.Remove(name); err != nil {
			fail(err)
			return
		}
		JSON(w, map[string]any{"ok": true}, http.StatusOK)
	default:
		http.NotFound(w, nil)
	}
}
