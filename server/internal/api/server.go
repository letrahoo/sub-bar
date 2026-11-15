package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/letrahoo/sub-cost-view/server/internal/model"
	"github.com/letrahoo/sub-cost-view/server/internal/oauth"
	"github.com/letrahoo/sub-cost-view/server/internal/poller"
	"github.com/letrahoo/sub-cost-view/server/internal/providers"
	"github.com/letrahoo/sub-cost-view/server/internal/store"
	"github.com/letrahoo/sub-cost-view/server/internal/web"
)

const Version = "0.2.0"

type Server struct {
	store    *store.Store
	registry *providers.Registry
	poller   *poller.Poller
	oauth    *oauth.Manager
	mux      *http.ServeMux
}

func New(st *store.Store, reg *providers.Registry, pl *poller.Poller) *Server {
	s := &Server{
		store:    st,
		registry: reg,
		poller:   pl,
		oauth:    oauth.NewManager(st, reg, pl),
		mux:      http.NewServeMux(),
	}
	s.routes()
	return s
}

func (s *Server) OAuthManager() *oauth.Manager {
	return s.oauth
}

func (s *Server) Shutdown() {
	if s.oauth != nil {
		s.oauth.Shutdown()
	}
}

func (s *Server) Handler() http.Handler {
	return s.securityMiddleware(s.mux)
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /api/health", s.handleHealth)
	s.mux.HandleFunc("GET /api/providers", s.handleProviders)
	s.mux.HandleFunc("GET /api/discover", s.handleDiscover)
	s.mux.HandleFunc("POST /api/discover/import", s.handleDiscoverImport)
	s.mux.HandleFunc("POST /api/oauth/start", s.handleOAuthStart)
	s.mux.HandleFunc("GET /api/oauth/status", s.handleOAuthStatus)
	s.mux.HandleFunc("GET /api/oauth/clients/{provider}", s.handleOAuthClientGet)
	s.mux.HandleFunc("PUT /api/oauth/clients/{provider}", s.handleOAuthClientPut)
	s.mux.HandleFunc("POST /api/oauth/callback-url", s.handleOAuthCallbackURL)
	s.mux.HandleFunc("GET /api/snapshot", s.handleSnapshot)
	s.mux.HandleFunc("GET /api/entries", s.handleListEntries)
	s.mux.HandleFunc("POST /api/entries", s.handleCreateEntry)
	s.mux.HandleFunc("PUT /api/entries/{id}", s.handleUpdateEntry)
	s.mux.HandleFunc("DELETE /api/entries/{id}", s.handleDeleteEntry)
	s.mux.HandleFunc("POST /api/entries/{id}/refresh", s.handleRefreshOne)
	s.mux.HandleFunc("POST /api/entries/{id}/quick-update", s.handleQuickUpdate)
	s.mux.HandleFunc("POST /api/refresh", s.handleRefreshAll)

	s.mux.Handle("/", web.Handler())
}

func (s *Server) securityMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}

		// Block cross-site browser requests (CSRF protection).
		if strings.EqualFold(r.Header.Get("Sec-Fetch-Site"), "cross-site") {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-site request rejected"})
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" && origin != "null" {
				u, err := url.Parse(origin)
				if err != nil || !sameOrLoopbackHost(u.Host, r.Host) {
					writeJSON(w, http.StatusForbidden, map[string]string{"error": "origin mismatch"})
					return
				}
			} else if origin == "null" {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "null origin rejected"})
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

func sameOrLoopbackHost(originHost, reqHost string) bool {
	if strings.EqualFold(originHost, reqHost) {
		return true
	}
	oh := strings.Split(strings.ToLower(originHost), ":")[0]
	rh := strings.Split(strings.ToLower(reqHost), ":")[0]
	isLoop := func(h string) bool { return h == "127.0.0.1" || h == "localhost" }
	return isLoop(oh) && isLoop(rh)
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	entries := s.store.ListEntries()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"version": Version,
		"entries": len(entries),
	})
}

func (s *Server) handleProviders(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"providers": s.registry.Descriptors(),
	})
}

func (s *Server) handleDiscover(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"hints": providers.DiscoverLocalHints(),
	})
}

func (s *Server) handleDiscoverImport(w http.ResponseWriter, r *http.Request) {
	var body struct {
		HintID string `json:"hintId"`
		Pinned bool   `json:"pinned"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	cfg, err := providers.ImportLocalDiscovery(r.Context(), strings.TrimSpace(body.HintID))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	cfg.Pinned = body.Pinned
	if err := s.normalizeEntry(&cfg); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	created, err := s.store.AddEntry(cfg)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	snap, _ := s.poller.RefreshOne(r.Context(), created.ID)
	if refreshed, ok := s.store.GetEntry(created.ID); ok {
		created = refreshed
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"entry":    created.Public(),
		"snapshot": snap,
	})
}

func (s *Server) handleOAuthStart(w http.ResponseWriter, r *http.Request) {
	var req oauth.StartRequest
	if err := decodeBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if req.DashboardURL != "" {
		u, err := url.Parse(strings.TrimSpace(req.DashboardURL))
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "官网面板链接必须是 http(s) URL"})
			return
		}
	}
	resp, err := s.oauth.Start(req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleOAuthStatus(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	resp, err := s.oauth.Status(state)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleOAuthCallbackURL(w http.ResponseWriter, r *http.Request) {
	var body struct {
		State       string `json:"state"`
		CallbackURL string `json:"callbackUrl"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	resp, err := s.oauth.SubmitCallbackInput(r.Context(), body.State, body.CallbackURL)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleSnapshot(w http.ResponseWriter, _ *http.Request) {
	resp := s.store.BuildSnapshotResponse(s.registry.NamesMap())
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleListEntries(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"entries": s.store.ListPublicEntries(),
	})
}

type entryWritePayload struct {
	model.EntryConfig
	KeepSecret bool `json:"keepSecret"`
}

func (s *Server) normalizeEntry(cfg *model.EntryConfig) error {
	cfg.Name = strings.TrimSpace(cfg.Name)
	if cfg.Name == "" {
		return errors.New("请填写入口名称")
	}
	prov, ok := s.registry.Get(cfg.Provider)
	if !ok {
		return errors.New("不支持的服务类型: " + cfg.Provider)
	}
	desc := prov.Descriptor()
	if !desc.SupportsKindSwitch || (cfg.Kind != model.KindSubscription && cfg.Kind != model.KindBalance) {
		cfg.Kind = desc.DefaultKind
	}
	cfg.Badge = strings.TrimSpace(cfg.Badge)
	if cfg.Badge == "" {
		cfg.Badge = desc.DefaultBadge
	}
	if cfg.RefreshSeconds <= 0 {
		cfg.RefreshSeconds = desc.DefaultRefreshSeconds
	}
	if cfg.RefreshSeconds < 30 {
		cfg.RefreshSeconds = 30
	}
	cfg.DashboardURL = strings.TrimSpace(cfg.DashboardURL)
	if cfg.DashboardURL != "" {
		u, err := url.Parse(cfg.DashboardURL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
			return errors.New("官网面板链接必须是 http(s) URL")
		}
	}
	return nil
}

func (s *Server) handleCreateEntry(w http.ResponseWriter, r *http.Request) {
	var payload entryWritePayload
	if err := decodeBody(r, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	cfg := payload.EntryConfig
	if err := s.normalizeEntry(&cfg); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	created, err := s.store.AddEntry(cfg)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	snap, _ := s.poller.RefreshOne(r.Context(), created.ID)
	writeJSON(w, http.StatusCreated, map[string]any{
		"entry":    created.Public(),
		"snapshot": snap,
	})
}

func (s *Server) handleUpdateEntry(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var payload entryWritePayload
	if err := decodeBody(r, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	cfg := payload.EntryConfig
	if err := s.normalizeEntry(&cfg); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	updated, err := s.store.UpdateEntry(id, cfg, payload.KeepSecret)
	if errors.Is(err, os.ErrNotExist) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "入口不存在"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	snap, _ := s.poller.RefreshOne(r.Context(), updated.ID)
	writeJSON(w, http.StatusOK, map[string]any{
		"entry":    updated.Public(),
		"snapshot": snap,
	})
}

func (s *Server) handleQuickUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	existing, ok := s.store.GetEntry(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "入口不存在"})
		return
	}
	var patch struct {
		Pinned        *bool    `json:"pinned"`
		Enabled       *bool    `json:"enabled"`
		ManualBalance *float64 `json:"manualBalance"`
		QuotaUsed     *float64 `json:"quotaUsed"`
		QuotaTotal    *float64 `json:"quotaTotal"`
	}
	if err := decodeBody(r, &patch); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if patch.Pinned != nil {
		existing.Pinned = *patch.Pinned
	}
	if patch.Enabled != nil {
		existing.Enabled = *patch.Enabled
	}
	if patch.ManualBalance != nil {
		existing.ManualBalance = patch.ManualBalance
	}
	if patch.QuotaUsed != nil {
		existing.QuotaUsed = patch.QuotaUsed
	}
	if patch.QuotaTotal != nil {
		existing.QuotaTotal = patch.QuotaTotal
	}
	updated, err := s.store.UpdateEntry(id, existing, true)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	var snap model.EntrySnapshot
	if existing.Provider == "manual" || (patch.Enabled != nil && *patch.Enabled) {
		snap, _ = s.poller.RefreshOne(r.Context(), updated.ID)
	} else {
		snap, _ = s.store.GetSnapshot(updated.ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"entry":    updated.Public(),
		"snapshot": snap,
	})
}

func (s *Server) handleDeleteEntry(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.DeleteEntry(id); errors.Is(err, os.ErrNotExist) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "入口不存在"})
		return
	} else if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleRefreshOne(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	snap, ok := s.poller.RefreshOne(r.Context(), id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "入口不存在"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"snapshot": snap})
}

func (s *Server) handleRefreshAll(w http.ResponseWriter, r *http.Request) {
	resp := s.poller.RefreshAll(r.Context())
	writeJSON(w, http.StatusOK, resp)
}

func decodeBody(r *http.Request, dst any) error {
	defer r.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(r.Body, 256<<10))
	if err != nil {
		return errors.New("读取请求体失败")
	}
	if len(raw) == 0 {
		return errors.New("请求体为空")
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return errors.New("JSON 格式无效: " + err.Error())
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	_ = enc.Encode(v)
}

func (s *Server) handleOAuthClientGet(w http.ResponseWriter, r *http.Request) {
	st, ok := providers.OAuthClientStatusFor(r.PathValue("provider"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "该服务不支持 OAuth"})
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleOAuthClientPut(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ClientID     string `json:"clientId"`
		ClientSecret string `json:"clientSecret"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	id := r.PathValue("provider")
	path := filepath.Join(s.store.Dir(), "oauth-clients.json")
	if err := providers.SetOAuthClient(path, id, req.ClientID, req.ClientSecret); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	st, _ := providers.OAuthClientStatusFor(id)
	writeJSON(w, http.StatusOK, st)
}
