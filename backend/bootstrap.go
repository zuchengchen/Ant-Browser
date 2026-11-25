package backend

import (
	appconfig "ant-chrome/backend/internal/config"
	apptray "ant-chrome/backend/internal/tray"
)

type Config = appconfig.Config
type TrayCallbacks = apptray.Callbacks

func LoadConfig(path string) (*Config, error) {
	return appconfig.Load(path)
}

func DefaultConfig() *Config {
	return appconfig.DefaultConfig()
}

func RunTray(cb TrayCallbacks) {
	apptray.Run(cb)
}

func QuitTray() {
	apptray.Quit()
}
