// Copyright 2023 The Gitea Authors. All rights reserved.
// Copyright 2023 The nektos/act Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package artifactcache

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gitea.com/gitea/runner/act/common"

	"github.com/julienschmidt/httprouter"
	"github.com/sirupsen/logrus"
	"github.com/timshannon/bolthold"
	"go.etcd.io/bbolt"
)

const (
	apiPath      = "/_apis/artifactcache"
	internalPath = "/_internal"

	// artifactURLTTL bounds how long a signed artifactLocation URL stays valid.
	// Short enough that a leaked URL is near-worthless; long enough to let the
	// @actions/cache client download a big blob that was returned from /cache.
	artifactURLTTL = 10 * time.Minute
)

type credKey struct{}

// JobCredential ties a per-job bearer token (ACTIONS_RUNTIME_TOKEN) to the
// repository that owns it. Every cache entry is stamped with Repo on
// reserve/commit and checked on read/write so one repo can never observe or
// poison another repo's cache, even from inside a container that reaches the
// cache server over the docker bridge network.
type JobCredential struct {
	Repo string
}

// credEntry holds a registered job's credential along with an active
// registration count. RegisterJob is reference-counted so that if two tasks
// briefly share an ACTIONS_RUNTIME_TOKEN — e.g. a runner that retries a task
// after a crash before the old registration is revoked — the first task's
// revoker does not cut the second task's auth out from under it.
type credEntry struct {
	cred JobCredential
	refs int
}

type Handler struct {
	dir      string
	storage  *Storage
	router   *httprouter.Router
	listener net.Listener
	server   *http.Server
	logger   logrus.FieldLogger

	gcing atomic.Bool
	gcAt  time.Time

	outboundIP string

	// internalSecret guards /_internal/{register,revoke}. When set, a remote
	// runner can use these endpoints to pre-register per-job
	// ACTIONS_RUNTIME_TOKENs against this server, enabling the same
	// per-job auth and repo scoping as the embedded handler over the
	// network. Empty disables the control-plane entirely.
	internalSecret string

	// secret signs short-lived artifact download URLs. The @actions/cache
	// toolkit does not send Authorization on the download request, so blob
	// GETs authenticate via a per-URL HMAC signature with expiry rather than
	// via the bearer token used for management endpoints.
	secret []byte

	credMu sync.RWMutex
	creds  map[string]*credEntry
}

// StartHandler opens the on-disk cache store and starts the HTTP server.
//
// internalSecret, when non-empty, enables a control-plane API at
// /_internal/{register,revoke} that lets a remote runner pre-register the
// per-job ACTIONS_RUNTIME_TOKENs it expects this server to honor. The
// embedded in-process handler leaves it empty and registers tokens via the
// in-process RegisterJob method directly.
func StartHandler(dir, outboundIP string, port uint16, internalSecret string, logger logrus.FieldLogger) (*Handler, error) {
	h := &Handler{
		creds:          make(map[string]*credEntry),
		internalSecret: internalSecret,
	}

	if logger == nil {
		discard := logrus.New()
		discard.Out = io.Discard
		logger = discard
	}
	logger = logger.WithField("module", "artifactcache")
	h.logger = logger

	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		dir = filepath.Join(home, ".cache", "actcache")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	h.dir = dir

	storage, err := NewStorage(filepath.Join(dir, "cache"))
	if err != nil {
		return nil, err
	}
	h.storage = storage

	if outboundIP != "" {
		h.outboundIP = outboundIP
	} else if ip := common.GetOutboundIP(); ip == nil {
		return nil, errors.New("unable to determine outbound IP address")
	} else {
		h.outboundIP = ip.String()
	}

	secret, err := loadOrCreateSecret(dir)
	if err != nil {
		return nil, err
	}
	h.secret = secret

	router := httprouter.New()
	router.GET(apiPath+"/cache", h.bearerAuth(h.find))
	router.POST(apiPath+"/caches", h.bearerAuth(h.reserve))
	router.PATCH(apiPath+"/caches/:id", h.bearerAuth(h.upload))
	router.POST(apiPath+"/caches/:id", h.bearerAuth(h.commit))
	router.POST(apiPath+"/clean", h.bearerAuth(h.clean))
	// Artifact GET is signed via query-string HMAC because @actions/cache
	// does not attach Authorization when downloading archiveLocation.
	router.GET(apiPath+"/artifacts/:id", h.signedURLAuth(h.get))
	// Control-plane: a remote runner registers/revokes per-job tokens so the
	// cache API can authenticate them. Always wired so the routes exist; the
	// handlers themselves 401 when internalSecret is unset.
	router.POST(internalPath+"/register", h.internalAuth(h.internalRegister))
	router.POST(internalPath+"/revoke", h.internalAuth(h.internalRevoke))

	h.router = router

	h.gcCache()

	// Listen on all interfaces. Binding to outboundIP only would give no real
	// security benefit (it is the LAN/internet-facing address either way) and
	// can break Docker Desktop variants where the host's outbound IP is not
	// routable from inside the container network. Authentication is enforced
	// by the bearer middleware and per-repo scoping, not by reachability.
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return nil, err
	}
	server := &http.Server{
		ReadHeaderTimeout: 2 * time.Second,
		Handler:           router,
	}
	go func() {
		if err := server.Serve(listener); err != nil && errors.Is(err, net.ErrClosed) {
			logger.Errorf("http serve: %v", err)
		}
	}()
	h.listener = listener
	h.server = server

	return h, nil
}

func (h *Handler) ExternalURL() string {
	// TODO: make the external url configurable if necessary
	return fmt.Sprintf("http://%s:%d",
		h.outboundIP,
		h.listener.Addr().(*net.TCPAddr).Port)
}

// RegisterJob makes token a valid bearer credential for cache requests from
// the given repository and returns a function that removes it. The runner
// calls this at job start and defers the returned func so that the credential
// is only accepted while the job is running.
//
// Registrations are reference-counted: if a token is already registered, the
// existing repo is kept and the refcount is incremented. The entry is
// removed only when every revoker returned by RegisterJob has been called.
// This keeps a stray re-registration from silently revoking a live job.
func (h *Handler) RegisterJob(token, repo string) func() {
	if h == nil || token == "" {
		return func() {}
	}
	h.credMu.Lock()
	if existing, ok := h.creds[token]; ok {
		existing.refs++
	} else {
		h.creds[token] = &credEntry{
			cred: JobCredential{Repo: repo},
			refs: 1,
		}
	}
	h.credMu.Unlock()
	return func() {
		h.credMu.Lock()
		if entry, ok := h.creds[token]; ok {
			entry.refs--
			if entry.refs <= 0 {
				delete(h.creds, token)
			}
		}
		h.credMu.Unlock()
	}
}

// RevokeJob explicitly revokes one registration of token, mirroring one call
// of the closure returned by RegisterJob. Used by the control-plane endpoint
// so a remote runner can revoke without holding the closure.
func (h *Handler) RevokeJob(token string) {
	if h == nil || token == "" {
		return
	}
	h.credMu.Lock()
	if entry, ok := h.creds[token]; ok {
		entry.refs--
		if entry.refs <= 0 {
			delete(h.creds, token)
		}
	}
	h.credMu.Unlock()
}

func (h *Handler) lookupCredential(token string) (JobCredential, bool) {
	h.credMu.RLock()
	entry, ok := h.creds[token]
	h.credMu.RUnlock()
	if !ok {
		return JobCredential{}, false
	}
	return entry.cred, true
}

// loadOrCreateSecret returns the 32-byte HMAC signing key for artifact URLs,
// persisted in dir/.secret so signed URLs handed out before a restart stay
// valid across the restart and so the standalone cache-server can be pointed
// at by config.Cache.ExternalServer without the URL rotating.
func loadOrCreateSecret(dir string) ([]byte, error) {
	path := filepath.Join(dir, ".secret")
	if data, err := os.ReadFile(path); err == nil {
		if secret, err := hex.DecodeString(strings.TrimSpace(string(data))); err == nil && len(secret) >= 32 {
			return secret, nil
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read cache secret: %w", err)
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("generate cache secret: %w", err)
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(secret)), 0o600); err != nil {
		return nil, fmt.Errorf("write cache secret: %w", err)
	}
	return secret, nil
}

func (h *Handler) Close() error {
	if h == nil {
		return nil
	}
	var retErr error
	if h.server != nil {
		err := h.server.Close()
		if err != nil {
			retErr = err
		}
		h.server = nil
	}
	if h.listener != nil {
		err := h.listener.Close()
		if errors.Is(err, net.ErrClosed) {
			err = nil
		}
		if err != nil {
			retErr = err
		}
		h.listener = nil
	}
	return retErr
}

func (h *Handler) openDB() (*bolthold.Store, error) {
	return bolthold.Open(filepath.Join(h.dir, "bolt.db"), 0o644, &bolthold.Options{
		Encoder: json.Marshal,
		Decoder: json.Unmarshal,
		Options: &bbolt.Options{
			Timeout:      5 * time.Second,
			NoGrowSync:   bbolt.DefaultOptions.NoGrowSync,
			FreelistType: bbolt.DefaultOptions.FreelistType,
		},
	})
}

// GET /_apis/artifactcache/cache
func (h *Handler) find(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	cred := credFromContext(r.Context())
	keys := strings.Split(r.URL.Query().Get("keys"), ",")
	version := r.URL.Query().Get("version")

	db, err := h.openDB()
	if err != nil {
		h.responseJSON(w, r, 500, err)
		return
	}
	defer db.Close()

	cache, err := findCache(db, cred.Repo, keys, version)
	if err != nil {
		h.responseJSON(w, r, 500, err)
		return
	}
	if cache == nil {
		h.responseJSON(w, r, 204)
		return
	}

	if ok, err := h.storage.Exist(cache.ID); err != nil {
		h.responseJSON(w, r, 500, err)
		return
	} else if !ok {
		_ = db.Delete(cache.ID, cache)
		h.responseJSON(w, r, 204)
		return
	}
	h.responseJSON(w, r, 200, map[string]any{
		"result":          "hit",
		"archiveLocation": h.signedArtifactURL(cache.ID, time.Now().Add(artifactURLTTL)),
		"cacheKey":        cache.Key,
	})
}

// POST /_apis/artifactcache/caches
func (h *Handler) reserve(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	cred := credFromContext(r.Context())
	api := &Request{}
	if err := json.NewDecoder(r.Body).Decode(api); err != nil {
		h.responseJSON(w, r, 400, err)
		return
	}

	cache := api.ToCache()
	cache.Repo = cred.Repo
	db, err := h.openDB()
	if err != nil {
		h.responseJSON(w, r, 500, err)
		return
	}
	defer db.Close()

	now := time.Now().Unix()
	cache.CreatedAt = now
	cache.UsedAt = now
	if err := insertCache(db, cache); err != nil {
		h.responseJSON(w, r, 500, err)
		return
	}
	h.responseJSON(w, r, 200, map[string]any{
		"cacheId": cache.ID,
	})
}

// PATCH /_apis/artifactcache/caches/:id
func (h *Handler) upload(w http.ResponseWriter, r *http.Request, params httprouter.Params) {
	cred := credFromContext(r.Context())
	id, err := strconv.ParseInt(params.ByName("id"), 10, 64)
	if err != nil {
		h.responseJSON(w, r, 400, err)
		return
	}

	cache := &Cache{}
	db, err := h.openDB()
	if err != nil {
		h.responseJSON(w, r, 500, err)
		return
	}
	defer db.Close()
	if err := db.Get(id, cache); err != nil {
		if errors.Is(err, bolthold.ErrNotFound) {
			h.responseJSON(w, r, 400, fmt.Errorf("cache %d: not reserved", id))
			return
		}
		h.responseJSON(w, r, 500, err)
		return
	}

	if cache.Repo != cred.Repo {
		h.responseJSON(w, r, 403, fmt.Errorf("cache %d: forbidden", id))
		return
	}

	if cache.Complete {
		h.responseJSON(w, r, 400, fmt.Errorf("cache %v %q: already complete", cache.ID, cache.Key))
		return
	}
	db.Close()
	start, _, err := parseContentRange(r.Header.Get("Content-Range"))
	if err != nil {
		h.responseJSON(w, r, 400, err)
		return
	}
	if err := h.storage.Write(cache.ID, start, r.Body); err != nil {
		h.responseJSON(w, r, 500, err)
		return
	}
	h.useCache(id)
	h.responseJSON(w, r, 200)
}

// POST /_apis/artifactcache/caches/:id
func (h *Handler) commit(w http.ResponseWriter, r *http.Request, params httprouter.Params) {
	cred := credFromContext(r.Context())
	id, err := strconv.ParseInt(params.ByName("id"), 10, 64)
	if err != nil {
		h.responseJSON(w, r, 400, err)
		return
	}

	cache := &Cache{}
	db, err := h.openDB()
	if err != nil {
		h.responseJSON(w, r, 500, err)
		return
	}
	defer db.Close()
	if err := db.Get(id, cache); err != nil {
		if errors.Is(err, bolthold.ErrNotFound) {
			h.responseJSON(w, r, 400, fmt.Errorf("cache %d: not reserved", id))
			return
		}
		h.responseJSON(w, r, 500, err)
		return
	}

	if cache.Repo != cred.Repo {
		h.responseJSON(w, r, 403, fmt.Errorf("cache %d: forbidden", id))
		return
	}

	if cache.Complete {
		h.responseJSON(w, r, 400, fmt.Errorf("cache %v %q: already complete", cache.ID, cache.Key))
		return
	}

	db.Close()

	size, err := h.storage.Commit(cache.ID, cache.Size)
	if err != nil {
		h.responseJSON(w, r, 500, err)
		return
	}
	// write real size back to cache, it may be different from the current value when the request doesn't specify it.
	cache.Size = size

	db, err = h.openDB()
	if err != nil {
		h.responseJSON(w, r, 500, err)
		return
	}
	defer db.Close()

	cache.Complete = true
	if err := db.Update(cache.ID, cache); err != nil {
		h.responseJSON(w, r, 500, err)
		return
	}

	h.responseJSON(w, r, 200)
}

// GET /_apis/artifactcache/artifacts/:id
// Authenticated via signed URL (see signedURLAuth), not bearer, because the
// @actions/cache toolkit downloads archiveLocation without Authorization.
// Repository scoping is already enforced at find() time; the signature binds
// the URL to the specific cache ID and an expiry.
func (h *Handler) get(w http.ResponseWriter, r *http.Request, params httprouter.Params) {
	id, err := strconv.ParseInt(params.ByName("id"), 10, 64)
	if err != nil {
		h.responseJSON(w, r, 400, err)
		return
	}
	h.useCache(id)
	h.storage.Serve(w, r, uint64(id))
}

// POST /_apis/artifactcache/clean
func (h *Handler) clean(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	// TODO: don't support force deleting cache entries
	// see: https://docs.github.com/en/actions/using-workflows/caching-dependencies-to-speed-up-workflows#force-deleting-cache-entries

	h.responseJSON(w, r, 200)
}

// bearerAuth resolves ACTIONS_RUNTIME_TOKEN against the set of currently
// registered jobs. A match attaches the job's JobCredential to the request
// context; a miss returns 401 before the handler body runs.
func (h *Handler) bearerAuth(handler httprouter.Handle) httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, params httprouter.Params) {
		h.logger.Debugf("%s %s", r.Method, r.URL.Path)
		token := bearerToken(r)
		if token == "" {
			h.responseJSON(w, r, http.StatusUnauthorized, errors.New("missing bearer token"))
			return
		}
		cred, ok := h.lookupCredential(token)
		if !ok {
			h.responseJSON(w, r, http.StatusUnauthorized, errors.New("unknown bearer token"))
			return
		}
		ctx := context.WithValue(r.Context(), credKey{}, cred)
		handler(w, r.WithContext(ctx), params)
		go h.gcCache()
	}
}

func (h *Handler) signedURLAuth(handler httprouter.Handle) httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, params httprouter.Params) {
		h.logger.Debugf("%s %s", r.Method, r.URL.Path)
		id, err := strconv.ParseInt(params.ByName("id"), 10, 64)
		if err != nil {
			h.responseJSON(w, r, 400, err)
			return
		}
		expStr := r.URL.Query().Get("exp")
		sig := r.URL.Query().Get("sig")
		if expStr == "" || sig == "" {
			h.responseJSON(w, r, http.StatusUnauthorized, errors.New("missing signature"))
			return
		}
		exp, err := strconv.ParseInt(expStr, 10, 64)
		if err != nil {
			h.responseJSON(w, r, http.StatusUnauthorized, errors.New("invalid expiry"))
			return
		}
		if time.Now().Unix() > exp {
			h.responseJSON(w, r, http.StatusUnauthorized, errors.New("signature expired"))
			return
		}
		expected := h.computeSignature(id, exp)
		if !hmac.Equal([]byte(sig), []byte(expected)) {
			h.responseJSON(w, r, http.StatusUnauthorized, errors.New("bad signature"))
			return
		}
		handler(w, r, params)
		go h.gcCache()
	}
}

// internalAuth gates the control-plane endpoints. The bearer must
// constant-time-equal the configured internalSecret. If the secret is empty,
// the control-plane is disabled and every request gets 404 — which matches
// the upstream nektos/act behavior of "the route does not exist".
func (h *Handler) internalAuth(handler httprouter.Handle) httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, params httprouter.Params) {
		if h.internalSecret == "" {
			http.NotFound(w, r)
			return
		}
		token := bearerToken(r)
		if token == "" || !hmac.Equal([]byte(token), []byte(h.internalSecret)) {
			h.responseJSON(w, r, http.StatusUnauthorized, errors.New("internal: bad secret"))
			return
		}
		handler(w, r, params)
	}
}

type internalRegisterBody struct {
	Token string `json:"token"`
	Repo  string `json:"repo"`
}

type internalRevokeBody struct {
	Token string `json:"token"`
}

// POST /_internal/register
func (h *Handler) internalRegister(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	var body internalRegisterBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		h.responseJSON(w, r, http.StatusBadRequest, err)
		return
	}
	if body.Token == "" {
		h.responseJSON(w, r, http.StatusBadRequest, errors.New("token is required"))
		return
	}
	h.RegisterJob(body.Token, body.Repo)
	h.responseJSON(w, r, http.StatusOK)
}

// POST /_internal/revoke
func (h *Handler) internalRevoke(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	var body internalRevokeBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		h.responseJSON(w, r, http.StatusBadRequest, err)
		return
	}
	if body.Token == "" {
		h.responseJSON(w, r, http.StatusBadRequest, errors.New("token is required"))
		return
	}
	h.RevokeJob(body.Token)
	h.responseJSON(w, r, http.StatusOK)
}

func bearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(auth) > len(prefix) && strings.EqualFold(auth[:len(prefix)], prefix) {
		return auth[len(prefix):]
	}
	return ""
}

func credFromContext(ctx context.Context) JobCredential {
	if cred, ok := ctx.Value(credKey{}).(JobCredential); ok {
		return cred
	}
	return JobCredential{}
}

func (h *Handler) computeSignature(cacheID, exp int64) string {
	mac := hmac.New(sha256.New, h.secret)
	fmt.Fprintf(mac, "%d:%d", cacheID, exp)
	return hex.EncodeToString(mac.Sum(nil))
}

func (h *Handler) signedArtifactURL(cacheID uint64, exp time.Time) string {
	expUnix := exp.Unix()
	sig := h.computeSignature(int64(cacheID), expUnix)
	q := url.Values{}
	q.Set("exp", strconv.FormatInt(expUnix, 10))
	q.Set("sig", sig)
	return fmt.Sprintf("%s%s/artifacts/%d?%s", h.ExternalURL(), apiPath, cacheID, q.Encode())
}

// if not found, return (nil, nil) instead of an error.
func findCache(db *bolthold.Store, repo string, keys []string, version string) (*Cache, error) {
	cache := &Cache{}
	for _, prefix := range keys {
		// if a key in the list matches exactly, don't return partial matches
		if err := db.FindOne(cache,
			bolthold.Where("Repo").Eq(repo).
				And("Key").Eq(prefix).
				And("Version").Eq(version).
				And("Complete").Eq(true).
				SortBy("CreatedAt").Reverse()); err == nil || !errors.Is(err, bolthold.ErrNotFound) {
			if err != nil {
				return nil, fmt.Errorf("find cache: %w", err)
			}
			return cache, nil
		}
		prefixPattern := "^" + regexp.QuoteMeta(prefix)
		re, err := regexp.Compile(prefixPattern)
		if err != nil {
			continue
		}
		if err := db.FindOne(cache,
			bolthold.Where("Repo").Eq(repo).
				And("Key").RegExp(re).
				And("Version").Eq(version).
				And("Complete").Eq(true).
				SortBy("CreatedAt").Reverse()); err != nil {
			if errors.Is(err, bolthold.ErrNotFound) {
				continue
			}
			return nil, fmt.Errorf("find cache: %w", err)
		}
		return cache, nil
	}
	return nil, nil //nolint:nilnil // pre-existing issue from nektos/act
}

func insertCache(db *bolthold.Store, cache *Cache) error {
	if err := db.Insert(bolthold.NextSequence(), cache); err != nil {
		return fmt.Errorf("insert cache: %w", err)
	}
	// write back id to db
	if err := db.Update(cache.ID, cache); err != nil {
		return fmt.Errorf("write back id to db: %w", err)
	}
	return nil
}

func (h *Handler) useCache(id int64) {
	db, err := h.openDB()
	if err != nil {
		return
	}
	defer db.Close()
	cache := &Cache{}
	if err := db.Get(id, cache); err != nil {
		return
	}
	cache.UsedAt = time.Now().Unix()
	_ = db.Update(cache.ID, cache)
}

const (
	keepUsed   = 30 * 24 * time.Hour
	keepUnused = 7 * 24 * time.Hour
	keepTemp   = 5 * time.Minute
	keepOld    = 5 * time.Minute
)

func (h *Handler) gcCache() {
	if h.gcing.Load() {
		return
	}
	if !h.gcing.CompareAndSwap(false, true) {
		return
	}
	defer h.gcing.Store(false)

	if time.Since(h.gcAt) < time.Hour {
		h.logger.Debugf("skip gc: %v", h.gcAt.String())
		return
	}
	h.gcAt = time.Now()
	h.logger.Debugf("gc: %v", h.gcAt.String())

	db, err := h.openDB()
	if err != nil {
		return
	}
	defer db.Close()

	// Remove the caches which are not completed for a while, they are most likely to be broken.
	var caches []*Cache
	if err := db.Find(&caches, bolthold.
		Where("UsedAt").Lt(time.Now().Add(-keepTemp).Unix()).
		And("Complete").Eq(false),
	); err != nil {
		h.logger.Warnf("find caches: %v", err)
	} else {
		for _, cache := range caches {
			h.storage.Remove(cache.ID)
			if err := db.Delete(cache.ID, cache); err != nil {
				h.logger.Warnf("delete cache: %v", err)
				continue
			}
			h.logger.Infof("deleted cache: %+v", cache)
		}
	}

	// Remove the old caches which have not been used recently.
	caches = caches[:0]
	if err := db.Find(&caches, bolthold.
		Where("UsedAt").Lt(time.Now().Add(-keepUnused).Unix()),
	); err != nil {
		h.logger.Warnf("find caches: %v", err)
	} else {
		for _, cache := range caches {
			h.storage.Remove(cache.ID)
			if err := db.Delete(cache.ID, cache); err != nil {
				h.logger.Warnf("delete cache: %v", err)
				continue
			}
			h.logger.Infof("deleted cache: %+v", cache)
		}
	}

	// Remove the old caches which are too old.
	caches = caches[:0]
	if err := db.Find(&caches, bolthold.
		Where("CreatedAt").Lt(time.Now().Add(-keepUsed).Unix()),
	); err != nil {
		h.logger.Warnf("find caches: %v", err)
	} else {
		for _, cache := range caches {
			h.storage.Remove(cache.ID)
			if err := db.Delete(cache.ID, cache); err != nil {
				h.logger.Warnf("delete cache: %v", err)
				continue
			}
			h.logger.Infof("deleted cache: %+v", cache)
		}
	}

	// Remove the old caches with the same key and version within the same
	// repository, keep the latest one. Aggregation must include Repo so two
	// repos that happen to share a (key, version) do not evict each other —
	// otherwise per-repo scoping holds for reads but one repo can age
	// another out after keepOld.
	// Also keep the olds which have been used recently for a while in case of the cache is still in use.
	if results, err := db.FindAggregate(
		&Cache{},
		bolthold.Where("Complete").Eq(true),
		"Repo", "Key", "Version",
	); err != nil {
		h.logger.Warnf("find aggregate caches: %v", err)
	} else {
		for _, result := range results {
			if result.Count() <= 1 {
				continue
			}
			result.Sort("CreatedAt")
			caches = caches[:0]
			result.Reduction(&caches)
			for _, cache := range caches[:len(caches)-1] {
				if time.Since(time.Unix(cache.UsedAt, 0)) < keepOld {
					// Keep it since it has been used recently, even if it's old.
					// Or it could break downloading in process.
					continue
				}
				h.storage.Remove(cache.ID)
				if err := db.Delete(cache.ID, cache); err != nil {
					h.logger.Warnf("delete cache: %v", err)
					continue
				}
				h.logger.Infof("deleted cache: %+v", cache)
			}
		}
	}
}

func (h *Handler) responseJSON(w http.ResponseWriter, r *http.Request, code int, v ...any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	var data []byte
	if len(v) == 0 || v[0] == nil {
		data, _ = json.Marshal(struct{}{})
	} else if err, ok := v[0].(error); ok {
		h.logger.Errorf("%v %v: %v", r.Method, r.URL.Path, err)
		data, _ = json.Marshal(map[string]any{
			"error": err.Error(),
		})
	} else {
		data, _ = json.Marshal(v[0])
	}
	w.WriteHeader(code)
	_, _ = w.Write(data)
}

func parseContentRange(s string) (int64, int64, error) {
	// support the format like "bytes 11-22/*" only
	s, _, _ = strings.Cut(strings.TrimPrefix(s, "bytes "), "/")
	s1, s2, _ := strings.Cut(s, "-")

	start, err := strconv.ParseInt(s1, 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("parse %q: %w", s, err)
	}
	stop, err := strconv.ParseInt(s2, 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("parse %q: %w", s, err)
	}
	return start, stop, nil
}
