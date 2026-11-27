package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Live is one owner-driven page: the owner logs in to a site from the phone
// («Вход на сайт»), the session stays in the site's profile and later
// headless runs and the GigaIDE wake reuse it. Passwords never pass through
// an AI: the owner types them into this page, nothing is logged.
//
// One live page at a time, closed after LiveIdle without input or LiveMax.
type Live struct {
	b       *cdp
	cancel  context.CancelFunc
	Site    string
	started time.Time
	mu      sync.Mutex
	touched time.Time
	viewed  time.Time // last frame request: the owner still has the page open
	seen    []string
}

const (
	LiveIdle = 4 * time.Minute
	// LiveUnwatched closes Chromium once the owner closed the page: the page
	// polls frames every 1.5 s while visible, so a long silence means nobody is looking
	// (24.11: a forgotten page held 9 processes and blocked MCPshka jobs).
	LiveUnwatched = 2 * time.Minute // room to fetch an SMS code in another app
	LiveMax       = 20 * time.Minute
	LiveWidth     = 412
	LiveHeight    = 820
)

var (
	liveMu  sync.Mutex
	current *Live
)

// StartLive opens (or reuses) the live page for site at its https root.
func StartLive(site string) (*Live, error) {
	if site = OperatorSite(site); site == "gigaide" {
		site = "gitverse.ru" // workspace not configured yet: open it from GitVerse
	}
	if !ValidSite(site) {
		return nil, errors.New("сайт: имя хоста, например gitverse.ru")
	}
	liveMu.Lock()
	defer liveMu.Unlock()
	if current != nil && current.alive() {
		if current.Site == site {
			current.touch()
			return current, nil
		}
		current.close()
	}
	if err := os.MkdirAll(ProfileDir(site), 0o700); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), LiveMax)
	b, err := launch(ctx, ProfileDir(site))
	if err != nil {
		cancel()
		return nil, err
	}
	b.site, b.free = site, true
	c, stop := context.WithTimeout(ctx, 40*time.Second)
	defer stop()
	if err := b.attach(c); err != nil {
		b.close()
		cancel()
		return nil, err
	}
	_, _ = b.call(c, "Emulation.setDeviceMetricsOverride", map[string]any{"width": LiveWidth, "height": LiveHeight, "deviceScaleFactor": 1, "mobile": true})
	load := b.expect("Page.loadEventFired")
	if _, err := b.call(c, "Page.navigate", map[string]any{"url": "https://" + site + "/"}); err != nil {
		b.close()
		cancel()
		return nil, err
	}
	select { // a slow site still opens; the frame shows its progress
	case <-load:
	case <-time.After(12 * time.Second):
	}
	l := &Live{b: b, cancel: cancel, Site: site, started: time.Now(), touched: time.Now(), viewed: time.Now()}
	current = l
	go l.reap(ctx)
	return l, nil
}

// CurrentLive is the open live page, if any.
func CurrentLive() *Live {
	liveMu.Lock()
	defer liveMu.Unlock()
	if current != nil && current.alive() {
		return current
	}
	return nil
}

// StopLive closes the live page; the login stays in the profile.
func StopLive() {
	liveMu.Lock()
	defer liveMu.Unlock()
	if current != nil {
		current.close()
		current = nil
	}
}

func (l *Live) reap(ctx context.Context) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			StopLiveIf(l)
			return
		case <-l.b.done:
			StopLiveIf(l)
			return
		case <-t.C:
			l.mu.Lock()
			idle := time.Since(l.touched) > LiveIdle || time.Since(l.viewed) > LiveUnwatched
			l.mu.Unlock()
			if idle {
				StopLiveIf(l)
				return
			}
		}
	}
}

// StopLiveIf closes l only if it is still the current page.
func StopLiveIf(l *Live) {
	liveMu.Lock()
	defer liveMu.Unlock()
	if current == l {
		current.close()
		current = nil
	}
}

func (l *Live) alive() bool {
	select {
	case <-l.b.done:
		return false
	default:
		return time.Since(l.started) < LiveMax
	}
}

func (l *Live) close() {
	l.b.close()
	l.cancel()
}

func (l *Live) touch() {
	l.mu.Lock()
	l.touched = time.Now()
	l.mu.Unlock()
}

func liveCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

// Frame is the current page as JPEG with its URL and title.
func (l *Live) Frame() (jpeg []byte, href, title string, err error) {
	l.mu.Lock()
	l.viewed = time.Now()
	l.mu.Unlock()
	c, cancel := liveCtx()
	defer cancel()
	raw, err := l.b.call(c, "Page.captureScreenshot", map[string]any{"format": "jpeg", "quality": 55})
	for try := 0; err != nil && strings.Contains(err.Error(), "Not attached") && try < 6; try++ {
		time.Sleep(500 * time.Millisecond) // the page is between documents
		raw, err = l.b.call(c, "Page.captureScreenshot", map[string]any{"format": "jpeg", "quality": 55})
	}
	if err != nil {
		return nil, "", "", err
	}
	var shot struct {
		Data string `json:"data"`
	}
	if err = json.Unmarshal(raw, &shot); err == nil {
		jpeg, err = base64.StdEncoding.DecodeString(shot.Data)
	}
	// Navigation history survives documents in motion, unlike page script.
	var h struct {
		CurrentIndex int `json:"currentIndex"`
		Entries      []struct {
			URL   string `json:"url"`
			Title string `json:"title"`
		} `json:"entries"`
	}
	if raw, e := l.b.call(c, "Page.getNavigationHistory", nil); e == nil && json.Unmarshal(raw, &h) == nil && h.CurrentIndex < len(h.Entries) {
		href, title = h.Entries[h.CurrentIndex].URL, h.Entries[h.CurrentIndex].Title
	}
	l.remember(href)
	return jpeg, href, title, err
}

// remember keeps the last pages the owner opened (e.g. the GigaIDE workspace
// URL) in live-seen.json for the operator; query strings are dropped.
func (l *Live) remember(href string) {
	if i := strings.IndexAny(href, "?#"); i >= 0 {
		href = href[:i]
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if href == "" || len(l.seen) > 0 && l.seen[len(l.seen)-1] == href {
		return
	}
	l.seen = append(l.seen, href)
	if len(l.seen) > 30 {
		l.seen = l.seen[1:]
	}
	body, _ := json.Marshal(map[string]any{"site": l.Site, "urls": l.seen, "at": time.Now().UTC()})
	_ = os.WriteFile(filepath.Join(StateDir, "live-seen.json"), body, 0o600)
}

// Tap clicks at page coordinates (CSS pixels of the LiveWidth viewport).
func (l *Live) Tap(x, y float64) error {
	l.touch()
	if x < 0 || y < 0 || x > LiveWidth || y > LiveHeight {
		return errors.New("точка вне страницы")
	}
	c, cancel := liveCtx()
	defer cancel()
	for _, kind := range []string{"mousePressed", "mouseReleased"} {
		if _, err := l.b.call(c, "Input.dispatchMouseEvent", map[string]any{"type": kind, "x": x, "y": y, "button": "left", "clickCount": 1}); err != nil {
			return err
		}
	}
	return nil
}

// Scroll moves the page by dy CSS pixels.
func (l *Live) Scroll(dy float64) error {
	l.touch()
	c, cancel := liveCtx()
	defer cancel()
	_, err := l.b.call(c, "Input.dispatchMouseEvent", map[string]any{"type": "mouseWheel", "x": LiveWidth / 2, "y": LiveHeight / 2, "deltaX": 0, "deltaY": dy})
	return err
}

// Type inserts text into the focused field.
func (l *Live) Type(text string) error {
	l.touch()
	if len(text) > 512 {
		return errors.New("слишком длинный текст")
	}
	c, cancel := liveCtx()
	defer cancel()
	_, err := l.b.call(c, "Input.insertText", map[string]any{"text": text})
	return err
}

var liveKeys = map[string][2]any{"Enter": {"\r", 13}, "Backspace": {"", 8}, "Tab": {"", 9}, "Escape": {"", 27}}

// Key presses Enter, Backspace, Tab or Escape.
func (l *Live) Key(name string) error {
	l.touch()
	k, ok := liveKeys[name]
	if !ok {
		return errors.New("клавиша: Enter, Backspace, Tab или Escape")
	}
	c, cancel := liveCtx()
	defer cancel()
	for _, kind := range []string{"keyDown", "keyUp"} {
		ev := map[string]any{"type": kind, "key": name, "code": name, "windowsVirtualKeyCode": k[1]}
		if kind == "keyDown" && k[0] != "" {
			ev["text"] = k[0]
		}
		if _, err := l.b.call(c, "Input.dispatchKeyEvent", ev); err != nil {
			return err
		}
	}
	return nil
}

// Goto opens an https URL or goes back ("back").
func (l *Live) Goto(raw string) error {
	l.touch()
	c, cancel := liveCtx()
	defer cancel()
	if raw == "back" {
		_, err := l.b.eval(c, "history.back(),true")
		return err
	}
	if !strings.HasPrefix(raw, "https://") {
		raw = "https://" + raw
	}
	if !requestAllowed(raw, l.Site, false) {
		return errors.New("только https")
	}
	raw2, err := l.b.call(c, "Page.navigate", map[string]any{"url": raw})
	if err == nil {
		err = navigationError(raw2)
	}
	return err
}
