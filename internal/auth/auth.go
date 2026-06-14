package auth

import (
	"path/filepath"

	"github.com/AlexxIT/go2rtc/internal/app"
	"github.com/rs/zerolog"
)

var log zerolog.Logger

// Init initialises the auth module.
func Init() {
	log = app.GetLogger("auth")

	// Resolve paths next to config file (or cwd)
	usersPath := "users.json"
	if app.ConfigPath != "" {
		usersPath = filepath.Join(filepath.Dir(app.ConfigPath), "users.json")
	}

	// JWT secret is persisted so tokens survive server restarts.
	// Stored as .jwt_secret in the same directory as users.json.
	secretPath := filepath.Join(filepath.Dir(usersPath), ".jwt_secret")
	if usersPath == "users.json" {
		secretPath = ".jwt_secret"
	}

	if err := initSecret(secretPath); err != nil {
		log.Fatal().Err(err).Msg("[auth] failed to init JWT secret")
	}

	if err := initStore(usersPath); err != nil {
		log.Fatal().Err(err).Msg("[auth] failed to load user store")
	}

	// Camera location store (non-fatal — no locations just means empty map)
	locPath := filepath.Join(filepath.Dir(usersPath), "camera_locations.json")
	if usersPath == "users.json" {
		locPath = "camera_locations.json"
	}
	if err := initLocations(locPath); err != nil {
		log.Warn().Err(err).Msg("[auth] camera locations load failed (continuing)")
	}

	// Camera groups store (non-fatal)
	groupsPath := filepath.Join(filepath.Dir(usersPath), "camera_groups.json")
	if usersPath == "users.json" {
		groupsPath = "camera_groups.json"
	}
	if err := initGroups(groupsPath); err != nil {
		log.Warn().Err(err).Msg("[auth] camera groups load failed (continuing)")
	}

	// App settings store (non-fatal)
	settingsPath := filepath.Join(filepath.Dir(usersPath), "settings.json")
	if usersPath == "users.json" {
		settingsPath = "settings.json"
	}
	if err := initSettings(settingsPath); err != nil {
		log.Warn().Err(err).Msg("[auth] settings load failed (continuing)")
	}

	// Camera types store (non-fatal)
	typesPath := filepath.Join(filepath.Dir(usersPath), "camera_types.json")
	if usersPath == "users.json" {
		typesPath = "camera_types.json"
	}
	if err := initCameraTypes(typesPath); err != nil {
		log.Warn().Err(err).Msg("[auth] camera types load failed (continuing)")
	}

	// Devices store + monitor (non-fatal)
	devicesPath := filepath.Join(filepath.Dir(usersPath), "devices.json")
	if usersPath == "users.json" {
		devicesPath = "devices.json"
	}
	if err := initDevices(devicesPath); err != nil {
		log.Warn().Err(err).Msg("[auth] devices load failed (continuing)")
	}
	eventsPath := filepath.Join(filepath.Dir(usersPath), "device_events.jsonl")
	if usersPath == "users.json" {
		eventsPath = "device_events.jsonl"
	}
	initDeviceMonitor(eventsPath)

	// Camera config presets store (non-fatal — seeds defaults on first run)
	presetsPath := filepath.Join(filepath.Dir(usersPath), "cam_presets.json")
	if usersPath == "users.json" {
		presetsPath = "cam_presets.json"
	}
	if err := initCamPresets(presetsPath); err != nil {
		log.Warn().Err(err).Msg("[auth] cam presets load failed (continuing)")
	}

	registerHandlers()
	registerProxyHandlers()
	registerLocationHandlers()
	registerGroupHandlers()
	registerSettingsHandler()
	registerCamerasHandler()
	registerCameraTypesHandler()
	registerDevicesHandler()
	registerCameraConfigHandler()
	registerCamPresetsHandler()

	log.Info().Str("users_file", usersPath).Str("secret_file", secretPath).Msg("[auth] ready")
}
