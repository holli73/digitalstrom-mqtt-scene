package modules

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"

	mqtt_base "github.com/eclipse/paho.mqtt.golang"
	"github.com/gaetancollaud/digitalstrom-mqtt/pkg/config"
	"github.com/gaetancollaud/digitalstrom-mqtt/pkg/digitalstrom"
	"github.com/gaetancollaud/digitalstrom-mqtt/pkg/homeassistant"
	"github.com/gaetancollaud/digitalstrom-mqtt/pkg/mqtt"
	"github.com/rs/zerolog/log"
)

const (
	scenes          string = "scenes"
	zones           string = "zones"
	sceneCommand    string = "scene"
	apartmentZoneId int    = 0
	broadcastGroup  int    = 0
	broadcastName   string = "all"
	maxSceneId      int    = 127
)

// Group names used by the scene events of version 1.x.
var sceneGroupNames = map[int]string{
	1: "light",
	2: "shade",
	3: "climate",
	4: "audio",
	5: "video",
	6: "safety",
	7: "access",
	8: "joker",
}

// Smarthome API zone applications mapped to the digitalSTROM group id, for
// the groups that get Home Assistant scene entities.
var sceneApplicationGroups = map[string]int{
	"lights":  1,
	"shades":  2,
	"awnings": 2,
	"audio":   4,
	"video":   5,
	"joker":   8,
}

// Standard scene numbers, see the scene commands in
// https://developer.digitalstrom.org/Architecture/ds-basics.pdf and
// https://github.com/openhab/openhab-addons/tree/main/bundles/org.openhab.binding.digitalstrom/src/main/java/org/openhab/binding/digitalstrom/internal/lib/structure/scene/constants
var (
	// Off and presets 1 to 4 of a group in a zone.
	zoneSceneEntities = []int{0, 5, 17, 18, 19}
	// Present, absent, sleeping, wakeup, standby, deep off and door bell,
	// called on the whole apartment.
	apartmentSceneEntities = []int{71, 72, 69, 70, 67, 68, 73}
	sceneEntityLabels      = map[int]string{
		0:  "Off",
		5:  "Preset 1",
		17: "Preset 2",
		18: "Preset 3",
		19: "Preset 4",
		67: "Standby",
		68: "Deep off",
		69: "Sleeping",
		70: "Wakeup",
		71: "Present",
		72: "Absent",
		73: "Door bell",
	}
	// Presets 2 to 4 are often unused, their entities are disabled by default.
	sceneEntitiesDisabledByDefault = map[int]bool{17: true, 18: true, 19: true}
)

// MQTT forbids the wildcards in published topics, and a '/' would add a topic
// level.
var sceneTopicReplacer = strings.NewReplacer("+", "_", "#", "_", "/", "_")

// SceneEvent is the scene call message of version 1.x, published on
// scenes/{zoneName}/{sceneName or sceneId}/event. Field names are kept as is
// (no JSON tags) to stay compatible with existing consumers.
type SceneEvent struct {
	ZoneId    int
	ZoneName  string
	GroupId   int
	GroupName string
	SceneId   int
	SceneName string
}

// Scenes Module forwards the scene calls of digitalSTROM to MQTT, with the
// same topic and payload as version 1.x, and calls the scenes published on
// zones/{zoneId}/{group}/scene/command. Scene calls are not available in the
// Smarthome API, so they use the legacy JSON API.
type ScenesModule struct {
	mqttClient mqtt.Client
	dsRegistry digitalstrom.Registry

	enabled             bool
	normalizeDeviceName bool
	dsOptions           *digitalstrom.ClientOptions
	legacyClient        digitalstrom.LegacyClient
}

func (c *ScenesModule) Start() error {
	if !c.enabled {
		log.Info().Msg("Scenes module disabled.")
		return nil
	}
	c.legacyClient = digitalstrom.NewLegacyClient(c.dsOptions)
	if err := c.legacyClient.SceneCallsStart(c.onSceneCall); err != nil {
		return err
	}
	topic := path.Join(zones, "+", "+", sceneCommand, mqtt.Command)
	return c.mqttClient.Subscribe(topic, func(_ mqtt_base.Client, message mqtt_base.Message) {
		// Scene calls are not idempotent (e.g. door bell, increment): never
		// replay a retained command on start or reconnect.
		if message.Retained() {
			log.Warn().Str("topic", message.Topic()).Msg("Ignoring retained scene command.")
			return
		}
		if err := c.onSceneCommand(message.Topic(), string(message.Payload())); err != nil {
			log.Error().
				Str("topic", message.Topic()).
				Str("payload", string(message.Payload())).
				Err(err).
				Msg("Error handling scene command.")
		}
	})
}

func (c *ScenesModule) Stop() error {
	if c.legacyClient != nil {
		c.legacyClient.SceneCallsStop()
	}
	return nil
}

func (c *ScenesModule) onSceneCall(call digitalstrom.SceneCall) {
	groupName, ok := sceneGroupNames[call.GroupId]
	if !ok {
		groupName = "unknown"
	}
	event := SceneEvent{
		ZoneId:    call.ZoneId,
		ZoneName:  call.ZoneName,
		GroupId:   call.GroupId,
		GroupName: groupName,
		SceneId:   call.SceneId,
		SceneName: call.SceneName,
	}
	log.Debug().
		Int("zoneId", event.ZoneId).
		Int("groupId", event.GroupId).
		Int("sceneId", event.SceneId).
		Str("scene", event.SceneName).
		Msg("Scene event")

	message, err := json.Marshal(event)
	if err != nil {
		log.Error().Err(err).Msg("Error serializing scene event")
		return
	}
	topic := c.sceneEventTopic(event)
	if err := c.mqttClient.Publish(topic, message); err != nil {
		log.Error().Err(err).Str("topic", topic).Msg("Error publishing scene event")
	}
}

func (c *ScenesModule) sceneEventTopic(event SceneEvent) string {
	zoneName := event.ZoneName
	if c.normalizeDeviceName {
		zoneName = normalizeForTopicName(zoneName)
	}
	sceneNameOrId := event.SceneName
	if sceneNameOrId == "" {
		// No name for the scene, take the id instead.
		sceneNameOrId = strconv.Itoa(event.SceneId)
	}
	return scenes + "/" + sceneTopicReplacer.Replace(zoneName) + "/" + sceneTopicReplacer.Replace(sceneNameOrId) + "/" + mqtt.Event
}

// onSceneCommand calls the scene number of the payload on the group and zone
// of the topic zones/{zoneId}/{group}/scene/command.
func (c *ScenesModule) onSceneCommand(topic string, payload string) error {
	levels := strings.Split(topic, "/")
	if len(levels) < 5 {
		return fmt.Errorf("unexpected topic %s", topic)
	}
	zoneId, err := strconv.Atoi(levels[len(levels)-4])
	if err != nil || !c.zoneExists(zoneId) {
		return fmt.Errorf("unknown zone id %s", levels[len(levels)-4])
	}
	groupId, ok := sceneGroupId(levels[len(levels)-3])
	if !ok {
		return fmt.Errorf("unknown group %s", levels[len(levels)-3])
	}
	sceneId, err := strconv.Atoi(strings.TrimSpace(payload))
	if err != nil || sceneId < 0 || sceneId > maxSceneId {
		return errors.New("the payload must be a scene number between 0 and 127")
	}
	log.Info().
		Int("zoneId", zoneId).
		Int("groupId", groupId).
		Int("sceneId", sceneId).
		Msg("Calling scene from MQTT command")
	return c.legacyClient.CallScene(zoneId, groupId, sceneId)
}

func (c *ScenesModule) zoneExists(zoneId int) bool {
	if zoneId == apartmentZoneId {
		return true
	}
	for _, zone := range c.dsRegistry.GetZones() {
		if zone.ZoneId == strconv.Itoa(zoneId) {
			return true
		}
	}
	return false
}

func sceneGroupId(name string) (int, bool) {
	if name == broadcastName {
		return broadcastGroup, true
	}
	for id, groupName := range sceneGroupNames {
		if groupName == name {
			return id, true
		}
	}
	return 0, false
}

func sceneGroupName(groupId int) string {
	if groupId == broadcastGroup {
		return broadcastName
	}
	return sceneGroupNames[groupId]
}

// GetHomeAssistantEntities returns a scene entity for the off and preset
// scenes of each group of each zone, and for the apartment scenes. The labels
// are the standard names: the custom scene names would need a legacy API call
// per scene at startup.
func (c *ScenesModule) GetHomeAssistantEntities() ([]homeassistant.DiscoveryConfig, error) {
	if !c.enabled {
		return nil, nil
	}
	var configs []homeassistant.DiscoveryConfig
	for _, sceneId := range apartmentSceneEntities {
		configs = append(configs, c.sceneEntity(apartmentZoneId, "Apartment", broadcastGroup, sceneId))
	}
	for _, zone := range c.dsRegistry.GetZones() {
		zoneId, err := strconv.Atoi(zone.ZoneId)
		if err != nil || zoneId == apartmentZoneId {
			continue
		}
		seen := map[int]bool{}
		for _, application := range zone.Attributes.Applications {
			groupId, ok := sceneApplicationGroups[application]
			if !ok || seen[groupId] {
				continue
			}
			seen[groupId] = true
			for _, sceneId := range zoneSceneEntities {
				configs = append(configs, c.sceneEntity(zoneId, zone.Attributes.Name, groupId, sceneId))
			}
		}
	}
	return configs, nil
}

// sceneDeviceId identifies the zone in Home Assistant. It contains the
// apartment id, so that several dSS can be connected to the same Home
// Assistant.
func (c *ScenesModule) sceneDeviceId(zoneId int) string {
	return normalizeForTopicName(c.dsRegistry.GetApartmentId()) + "_zone_" + strconv.Itoa(zoneId)
}

func (c *ScenesModule) sceneEntity(zoneId int, zoneName string, groupId int, sceneId int) homeassistant.DiscoveryConfig {
	deviceId := c.sceneDeviceId(zoneId)
	group := sceneGroupName(groupId)
	objectId := "scene_" + group + "_" + strconv.Itoa(sceneId)
	name := sceneEntityLabels[sceneId]
	if groupId != broadcastGroup {
		name = strings.ToUpper(group[:1]) + group[1:] + " " + name
	}
	return homeassistant.DiscoveryConfig{
		Domain:   homeassistant.Scene,
		DeviceId: deviceId,
		ObjectId: objectId,
		Config: &homeassistant.SceneConfig{
			BaseConfig: homeassistant.BaseConfig{
				Device: homeassistant.Device{
					Identifiers: []string{deviceId},
					Model:       "Zone",
					Name:        zoneName,
				},
				Name:     name,
				UniqueId: deviceId + "_" + objectId,
			},
			CommandTopic: c.mqttClient.GetFullTopic(
				path.Join(zones, strconv.Itoa(zoneId), group, sceneCommand, mqtt.Command)),
			PayloadOn:        strconv.Itoa(sceneId),
			Icon:             "mdi:palette",
			EnabledByDefault: !sceneEntitiesDisabledByDefault[sceneId],
		},
	}
}

func NewScenesModule(mqttClient mqtt.Client, _ digitalstrom.Client, dsRegistry digitalstrom.Registry, config *config.Config) Module {
	return &ScenesModule{
		mqttClient:          mqttClient,
		dsRegistry:          dsRegistry,
		enabled:             config.ScenesEnabled,
		normalizeDeviceName: config.Mqtt.NormalizeDeviceName,
		dsOptions: digitalstrom.NewClientOptions().
			SetHost(config.Digitalstrom.Host).
			SetPort(config.Digitalstrom.Port).
			SetApiKey(config.Digitalstrom.ApiKey),
	}
}

func init() {
	Register(scenes, NewScenesModule)
}
