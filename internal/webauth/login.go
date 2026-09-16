package webauth

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/JavoxirJava/telegram-gateway/internal/audit"
	"github.com/JavoxirJava/telegram-gateway/internal/requestinfo"
	"github.com/google/uuid"
	"github.com/skip2/go-qrcode"
)

//go:embed login.html
var loginHTML string
var loginTemplate = template.Must(template.New("login").Parse(loginHTML))

func (s *Service) loginPage(w http.ResponseWriter, r *http.Request) {
	next := safeNext(r.URL.Query().Get("return_to"))
	if _, err := s.Authenticate(r); err == nil {
		http.Redirect(w, r, next, http.StatusSeeOther)
		return
	}
	token := random()
	s.cookie(w, "login", token, 10*60)
	s.headers(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = loginTemplate.Execute(w, map[string]string{"CSRF": token, "Next": next})
	})).ServeHTTP(w, r)
}
func (s *Service) loginRoute(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(s.cookieName("login"))
	if err != nil || len(cookie.Value) != 43 || r.Header.Get("X-CSRF-Token") != cookie.Value || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != s.base) {
		failure(w, 403, "Kirish sahifasini qayta oching.")
		return
	}
	key := base64.RawURLEncoding.EncodeToString(digest(cookie.Value))
	ip := requestinfo.ClientIP(r)
	if !s.allow(r, "rl:login-poll:"+key, 20, 2) {
		failure(w, 429, "Biroz kutib qayta urinib ko‘ring.")
		return
	}
	if r.Method == "POST" && r.URL.Path == "/auth/login/start" {
		if !s.allow(r, "rl:login-start:"+ip, 5, 1.0/60) {
			failure(w, 429, "Kirish urinishlari ko‘paydi. Bir daqiqa kuting.")
			return
		}
		var in struct {
			Next string `json:"return_to"`
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			failure(w, 400, "Noto‘g‘ri so‘rov.")
			return
		}
		s.mu.Lock()
		a := s.attempts[key]
		if a == nil {
			sameIP := 0
			for _, other := range s.attempts {
				if other.ip == ip {
					sameIP++
				}
			}
			if len(s.attempts) >= 20 || sameIP >= 5 {
				s.mu.Unlock()
				failure(w, 429, "Hozir kirish navbati band. Keyinroq qayta urinib ko‘ring.")
				return
			}
			a = &attempt{storage: uuid.NewString(), ip: ip, next: safeNext(in.Next), expires: time.Now().Add(10 * time.Minute)}
			a.mu.Lock()
			s.attempts[key] = a
			s.mu.Unlock()
			a.session, err = s.backend.NewLoginSession(s.ctx, a.storage)
			a.mu.Unlock()
			if err != nil {
				s.mu.Lock()
				delete(s.attempts, key)
				s.mu.Unlock()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				s.backend.DiscardLoginStorage(ctx, a.storage)
				cancel()
				s.logger.Error("start public Telegram login", "error", err)
				failure(w, 503, "Telegram ulanishini boshlash imkoni bo‘lmadi.")
				return
			}
		} else {
			s.mu.Unlock()
		}
		response(w, 200, map[string]string{"status": "started"})
		return
	}
	s.mu.Lock()
	a := s.attempts[key]
	s.mu.Unlock()
	if a == nil || time.Now().After(a.expires) {
		failure(w, 410, "Kirish muddati tugadi. Sahifani qayta oching.")
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || a.issued || time.Now().After(a.expires) {
		failure(w, 410, "Kirish yakunlangan yoki muddati tugagan.")
		return
	}
	if a.session == nil {
		failure(w, 503, "Telegram ulanishi boshlanmoqda.")
		return
	}
	if r.Method == "POST" && r.URL.Path == "/auth/login/action" {
		if !s.allow(r, "rl:login-action:"+ip, 10, 1.0/6) {
			failure(w, 429, "Biroz kutib qayta urinib ko‘ring.")
			return
		}
		var in struct {
			Action string `json:"action"`
			Value  string `json:"value"`
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil || len(in.Value) > 1024 || a.complete || a.profile != nil {
			failure(w, 400, "Noto‘g‘ri so‘rov.")
			return
		}
		if in.Action != "password" {
			in.Value = strings.TrimSpace(in.Value)
		}
		if err := a.session.Authorize(r.Context(), in.Action, in.Value); err != nil {
			failure(w, 400, err.Error())
			return
		}
		response(w, 200, map[string]string{"status": "accepted"})
		return
	}
	if r.Method != "GET" || r.URL.Path != "/auth/login/state" {
		http.NotFound(w, r)
		return
	}
	if a.profile == nil && a.session.IsReady() {
		profile, err := a.session.Profile(r.Context())
		if err != nil || profile.TelegramUserID <= 0 {
			failure(w, 503, "Telegram akkauntini tekshirish imkoni bo‘lmadi.")
			return
		}
		a.profile = &profile
		a.session.Close()
	}
	if a.profile != nil {
		if a.account == "" {
			ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
			defer cancel()
			a.user, a.account, err = s.backend.AttachLogin(ctx, a.storage, *a.profile)
			if err != nil {
				s.logger.Error("attach verified Telegram login", "error", err)
				failure(w, 503, "Akkauntni ulash imkoni bo‘lmadi. Qayta urinib ko‘ring.")
				return
			}
		}
		if !a.complete {
			if err := audit.NewWriter(s.pool).Write(r.Context(), audit.Event{AccountID: &a.account, ActorType: audit.ActorSystem, ActorID: "telegram-login", Action: "USER_TELEGRAM_LOGIN", ResourceType: "telegram_account", ResourceID: a.account}); err != nil {
				failure(w, 503, "Kirishni yakunlab bo‘lmadi.")
				return
			}
			a.complete = true
		}
		if err = s.IssueCookie(r.Context(), w, a.user, a.account); err != nil {
			failure(w, 503, "Kirishni yakunlab bo‘lmadi.")
			return
		}
		a.issued = true
		s.cookie(w, "login", "", -1)
		s.mu.Lock()
		delete(s.attempts, key)
		s.mu.Unlock()
		response(w, 200, map[string]any{"state": "complete", "redirect": a.next})
		return
	}
	state := a.session.State()
	if link, ok := state["link"].(string); ok && link != "" {
		if png, err := qrcode.Encode(link, qrcode.Medium, 256); err == nil {
			state["qr_image"] = "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
		}
	}
	response(w, 200, state)
}
