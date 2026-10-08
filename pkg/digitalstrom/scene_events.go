package digitalstrom

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// The Smarthome API notifications don't carry any information about scene
// calls. Scene calls are only available through the legacy JSON event API
// (json/event/subscribe and json/event/get), which is still served by the
// dSS. The API key is an application token, which allows to open a session on
// the legacy API using json/system/loginApplication.

const (
	sceneEventsPollTimeout     = 25 * time.Second
	sceneEventsReconnectDelay  = 5 * time.Second
	sceneEventsMaxReconnect    = 60 * time.Second
	sceneEventsHttpTimeout     = sceneEventsPollTimeout + 15*time.Second
	sceneEventsSubscriptionMin = 1000
)

// SceneEvent is a scene call (or undo) reported by the digitalSTROM server.
type SceneEvent struct {
	Event    EventType
	ZoneId   int
	GroupId  int
	SceneId  int
	Forced   bool
	OriginId string
}

type SceneEventCallback func(event SceneEvent)

// SceneEventListener is implemented by clients that can report scene calls.
type SceneEventListener interface {
	SceneEventsStart(callback SceneEventCallback) error
	SceneEventsStop()
	// SceneName returns the user defined name of a scene, or an empty string
	// if the scene has no custom name.
	SceneName(zoneId int, groupId int, sceneId int) (string, error)
	// ZoneName returns the name of a zone as known by the legacy JSON API.
	ZoneName(zoneId int) (string, error)
}

type legacyResponse struct {
	Ok      bool            `json:"ok"`
	Message string          `json:"message"`
	Result  json.RawMessage `json:"result"`
}

type legacyEvent struct {
	Name       string                 `json:"name"`
	Properties map[string]interface{} `json:"properties"`
	Source     map[string]interface{} `json:"source"`
}

type sceneEvents struct {
	options        ClientOptions
	httpClient     *http.Client
	subscriptionId int

	mu        sync.Mutex
	token     string
	cancel    context.CancelFunc
	done      chan struct{}
	nameCache map[string]string
}

func newSceneEvents(options ClientOptions) *sceneEvents {
	return &sceneEvents{
		options: options,
		httpClient: &http.Client{
			Timeout: sceneEventsHttpTimeout,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: true,
				},
			},
		},
		subscriptionId: sceneEventsSubscriptionMin + rand.Intn(1000000),
		nameCache:      map[string]string{},
	}
}

func (c *client) SceneEventsStart(callback SceneEventCallback) error {
	return c.sceneEvents.start(callback)
}

func (c *client) SceneEventsStop() {
	c.sceneEvents.stop()
}

func (c *client) SceneName(zoneId int, groupId int, sceneId int) (string, error) {
	return c.sceneEvents.sceneName(zoneId, groupId, sceneId)
}

func (c *client) ZoneName(zoneId int) (string, error) {
	return c.sceneEvents.zoneName(zoneId)
}

func (s *sceneEvents) start(callback SceneEventCallback) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		return errors.New("scene events listener already started")
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.done = make(chan struct{})
	go s.run(ctx, callback)
	return nil
}

func (s *sceneEvents) stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.cancel = nil
	s.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
	s.unsubscribe()
}

func (s *sceneEvents) run(ctx context.Context, callback SceneEventCallback) {
	defer close(s.done)
	delay := sceneEventsReconnectDelay
	subscribed := false
	for ctx.Err() == nil {
		if !subscribed {
			if err := s.subscribe(ctx); err != nil {
				if ctx.Err() != nil {
					return
				}
				log.Warn().Err(err).Dur("retryIn", delay).Msg("Unable to subscribe to digitalSTROM scene events")
				if !sleepContext(ctx, delay) {
					return
				}
				delay = min(delay*2, sceneEventsMaxReconnect)
				continue
			}
			log.Info().Int("subscriptionId", s.subscriptionId).Msg("Subscribed to digitalSTROM scene events")
			subscribed = true
			delay = sceneEventsReconnectDelay
		}

		events, err := s.poll(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Warn().Err(err).Msg("Error polling digitalSTROM scene events, will resubscribe")
			subscribed = false
			if !sleepContext(ctx, delay) {
				return
			}
			continue
		}
		for _, event := range events {
			if sceneEvent, ok := parseSceneEvent(event); ok {
				callback(sceneEvent)
			}
		}
	}
}

func (s *sceneEvents) subscribe(ctx context.Context) error {
	token, err := s.login(ctx)
	if err != nil {
		return err
	}
	for _, name := range []EventType{EventTypeCallScene, EventTypeUndoScene} {
		params := url.Values{}
		params.Set("name", string(name))
		params.Set("subscriptionID", strconv.Itoa(s.subscriptionId))
		params.Set("token", token)
		if _, err := s.request(ctx, "json/event/subscribe", params); err != nil {
			return fmt.Errorf("error subscribing to %s: %w", name, err)
		}
	}
	return nil
}

func (s *sceneEvents) unsubscribe() {
	s.mu.Lock()
	token := s.token
	s.mu.Unlock()
	if token == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, name := range []EventType{EventTypeCallScene, EventTypeUndoScene} {
		params := url.Values{}
		params.Set("name", string(name))
		params.Set("subscriptionID", strconv.Itoa(s.subscriptionId))
		params.Set("token", token)
		_, _ = s.request(ctx, "json/event/unsubscribe", params)
	}
}

func (s *sceneEvents) login(ctx context.Context) (string, error) {
	params := url.Values{}
	params.Set("loginToken", s.options.ApiKey)
	result, err := s.request(ctx, "json/system/loginApplication", params)
	if err != nil {
		return "", fmt.Errorf("error logging in on the digitalSTROM JSON API: %w", err)
	}
	var login struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(result, &login); err != nil || login.Token == "" {
		return "", errors.New("no session token returned by json/system/loginApplication")
	}
	s.mu.Lock()
	s.token = login.Token
	s.mu.Unlock()
	return login.Token, nil
}

func (s *sceneEvents) currentToken(ctx context.Context) (string, error) {
	s.mu.Lock()
	token := s.token
	s.mu.Unlock()
	if token != "" {
		return token, nil
	}
	return s.login(ctx)
}

func (s *sceneEvents) poll(ctx context.Context) ([]legacyEvent, error) {
	s.mu.Lock()
	token := s.token
	s.mu.Unlock()
	params := url.Values{}
	params.Set("subscriptionID", strconv.Itoa(s.subscriptionId))
	params.Set("timeout", strconv.Itoa(int(sceneEventsPollTimeout.Milliseconds())))
	params.Set("token", token)
	result, err := s.request(ctx, "json/event/get", params)
	if err != nil {
		return nil, err
	}
	var response struct {
		Events []legacyEvent `json:"events"`
	}
	if len(result) > 0 {
		if err := json.Unmarshal(result, &response); err != nil {
			return nil, fmt.Errorf("error decoding events: %w", err)
		}
	}
	log.Trace().Int("count", len(response.Events)).Msg("Scene events received")
	return response.Events, nil
}

func (s *sceneEvents) sceneName(zoneId int, groupId int, sceneId int) (string, error) {
	key := fmt.Sprintf("%d/%d/%d", zoneId, groupId, sceneId)
	s.mu.Lock()
	name, ok := s.nameCache[key]
	s.mu.Unlock()
	if ok {
		return name, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	token, err := s.currentToken(ctx)
	if err != nil {
		return "", err
	}
	params := url.Values{}
	params.Set("id", strconv.Itoa(zoneId))
	params.Set("groupID", strconv.Itoa(groupId))
	params.Set("sceneNumber", strconv.Itoa(sceneId))
	params.Set("token", token)
	result, err := s.request(ctx, "json/zone/sceneGetName", params)
	if err != nil {
		return "", err
	}
	var response struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(result, &response)
	name = strings.TrimSpace(response.Name)
	s.mu.Lock()
	s.nameCache[key] = name
	s.mu.Unlock()
	return name, nil
}

func (s *sceneEvents) zoneName(zoneId int) (string, error) {
	key := fmt.Sprintf("zone/%d", zoneId)
	s.mu.Lock()
	name, ok := s.nameCache[key]
	s.mu.Unlock()
	if ok {
		return name, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	token, err := s.currentToken(ctx)
	if err != nil {
		return "", err
	}
	params := url.Values{}
	params.Set("id", strconv.Itoa(zoneId))
	params.Set("token", token)
	result, err := s.request(ctx, "json/zone/getName", params)
	if err != nil {
		return "", err
	}
	var response struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(result, &response)
	name = response.Name
	s.mu.Lock()
	s.nameCache[key] = name
	s.mu.Unlock()
	return name, nil
}

// request calls the legacy JSON API and returns the content of the "result"
// field.
func (s *sceneEvents) request(ctx context.Context, path string, params url.Values) (json.RawMessage, error) {
	callUrl := "https://" + s.options.Host + ":" + strconv.Itoa(s.options.Port) + "/" + path + "?" + params.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, callUrl, nil)
	if err != nil {
		return nil, fmt.Errorf("error building the request: %w", err)
	}
	resp, err := s.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("error doing the request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("error reading the response: %w", err)
	}
	if resp.StatusCode >= 300 {
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
			s.mu.Lock()
			s.token = ""
			s.mu.Unlock()
		}
		return nil, responseError(resp.StatusCode)
	}
	var response legacyResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("error parsing response for path %s: %w", path, err)
	}
	if !response.Ok {
		return nil, fmt.Errorf("request %s failed: %s", path, response.Message)
	}
	return response.Result, nil
}

func parseSceneEvent(event legacyEvent) (SceneEvent, bool) {
	name := EventType(event.Name)
	if name != EventTypeCallScene && name != EventTypeUndoScene {
		return SceneEvent{}, false
	}
	sceneId, ok := intField(event.Properties, "sceneID")
	if !ok {
		return SceneEvent{}, false
	}
	zoneId, ok := intField(event.Properties, "zoneID")
	if !ok {
		zoneId, _ = intField(event.Source, "zoneID")
	}
	groupId, ok := intField(event.Properties, "groupID")
	if !ok {
		groupId, _ = intField(event.Source, "groupID")
	}
	forced, _ := event.Properties["forced"].(string)
	origin, _ := event.Properties["originDSUID"].(string)
	return SceneEvent{
		Event:    name,
		ZoneId:   zoneId,
		GroupId:  groupId,
		SceneId:  sceneId,
		Forced:   forced == "true",
		OriginId: origin,
	}, true
}

// intField reads a number that the dSS sends either as string or as number.
func intField(values map[string]interface{}, key string) (int, bool) {
	switch v := values[key].(type) {
	case float64:
		return int(v), true
	case string:
		i, err := strconv.Atoi(v)
		return i, err == nil
	}
	return 0, false
}

func sleepContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
