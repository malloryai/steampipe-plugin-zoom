package zoom

import "github.com/turbot/steampipe-plugin-sdk/v5/plugin"

type zoomConfig struct {
	AccountID    *string `hcl:"account_id"`
	ClientID     *string `hcl:"client_id"`
	ClientSecret *string `hcl:"client_secret"`
}

func ConfigInstance() interface{} { return &zoomConfig{} }
func GetConfig(connection *plugin.Connection) zoomConfig {
	if connection == nil {
		return zoomConfig{}
	}
	config, _ := connection.Config.(zoomConfig)
	return config
}
