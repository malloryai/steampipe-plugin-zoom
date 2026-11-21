package zoom

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/turbot/steampipe-plugin-sdk/v5/grpc/proto"
	"github.com/turbot/steampipe-plugin-sdk/v5/plugin"
	"github.com/turbot/steampipe-plugin-sdk/v5/plugin/transform"
)

var clientCacheMu sync.Mutex

func connect(ctx context.Context, d *plugin.QueryData) (*client, error) {
	cfg := GetConfig(d.Connection)
	candidate, err := newClient(cfg)
	if err != nil {
		return nil, err
	}
	if d.ConnectionCache == nil {
		return candidate, nil
	}
	// The SDK cache is connection-scoped. Include all credential fields to isolate rotations.
	encoded, _ := json.Marshal([]string{candidate.accountID, candidate.clientID, candidate.clientSecret})
	key := fmt.Sprintf("zoom-client-%x", sha256.Sum256(encoded))
	clientCacheMu.Lock()
	defer clientCacheMu.Unlock()
	if cached, ok := d.ConnectionCache.Get(ctx, key); ok {
		return cached.(*client), nil
	}
	if err := d.ConnectionCache.Set(ctx, key, candidate); err != nil {
		return nil, errors.New("Zoom connection cache could not be initialized")
	}
	return candidate, nil
}

func Plugin(context.Context) *plugin.Plugin {
	p := &plugin.Plugin{Name: "steampipe-plugin-zoom", ConnectionConfigSchema: &plugin.ConnectionConfigSchema{NewInstance: ConfigInstance}, TableMap: map[string]*plugin.Table{}}
	for name, spec := range tableSpecs() {
		name, spec := name, spec
		p.TableMap[name] = &plugin.Table{Name: name, Description: spec.description, List: &plugin.ListConfig{KeyColumns: spec.keys, Hydrate: func(ctx context.Context, d *plugin.QueryData, _ *plugin.HydrateData) (interface{}, error) {
			c, err := connect(ctx, d)
			if err != nil {
				return nil, err
			}
			opts := scanOptions{}
			if spec.parentKey != "" {
				opts.parentID = d.EqualsQuals[spec.parentKey].GetStringValue()
			}
			if name == "zoom_user" {
				opts.status = d.EqualsQuals["status"].GetStringValue()
			}
			if spec.logs {
				var bounds []timeBound
				if qualifiers := d.Quals["time"]; qualifiers != nil {
					for _, qual := range qualifiers.Quals {
						stamp := qual.Value.GetTimestampValue()
						if stamp == nil || stamp.CheckValid() != nil {
							return nil, errors.New("Zoom log time bound must be a timestamp")
						}
						bounds = append(bounds, timeBound{qual.Operator, stamp.AsTime()})
					}
				}
				opts.start, opts.end, err = timeBounds(time.Now(), bounds)
				if err != nil {
					return nil, err
				}
			}
			err = stageRows(ctx, func(emit func(row) error) error {
				return c.scan(ctx, name, opts, func(raw row) error {
					selected := row{}
					for _, column := range spec.columns {
						if value, ok := raw[column.Name]; ok {
							selected[column.Name] = value
						}
					}
					return emit(selected)
				})
			}, func(item row) error { d.StreamListItem(ctx, item); return nil })
			return nil, err
		}}, Columns: spec.columns}
	}
	return p
}

func column(name string, kind proto.ColumnType, description string) *plugin.Column {
	return &plugin.Column{Name: name, Type: kind, Description: description, Transform: transform.FromField(name)}
}
func stringsColumns(names ...string) []*plugin.Column {
	var result []*plugin.Column
	for _, name := range names {
		result = append(result, column(name, proto.ColumnType_STRING, "Zoom "+name+" field."))
	}
	return result
}
func optional(name string) plugin.KeyColumnSlice {
	return plugin.KeyColumnSlice{{Name: name, Require: plugin.Optional}}
}

type tableSpec struct {
	description string
	columns     []*plugin.Column
	keys        plugin.KeyColumnSlice
	parentKey   string
	logs        bool
}

func tableSpecs() map[string]tableSpec {
	return map[string]tableSpec{
		"zoom_user": {
			description: "Zoom users across active, inactive and pending populations.", keys: optional("status"),
			columns: append(stringsColumns("id", "first_name", "last_name", "email", "status", "dept", "last_client_version", "timezone"), column("type", proto.ColumnType_INT, "Zoom user plan type."), column("created_at", proto.ColumnType_TIMESTAMP, "Account creation time."), column("last_login_time", proto.ColumnType_TIMESTAMP, "Last login time reported by Zoom.")),
		},
		"zoom_group":         {description: "Zoom account groups.", columns: append(stringsColumns("id", "name"), column("total_members", proto.ColumnType_INT, "Number of members reported by Zoom."))},
		"zoom_group_member":  {description: "Zoom group memberships. With no group_id filter, enumerate every group.", parentKey: "group_id", keys: optional("group_id"), columns: append(stringsColumns("id", "group_id", "email", "first_name", "last_name"), column("type", proto.ColumnType_INT, "Zoom user plan type."))},
		"zoom_device":        {description: "Zoom managed devices across supported device type, enrollment and status partitions.", columns: append(stringsColumns("device_id", "device_name", "app_version", "platform_os", "model", "vendor", "serial_number", "mac_address", "room_id", "room_name", "sdk_version", "user_email", "tag"), column("device_type", proto.ColumnType_INT, "Zoom device type."), column("device_status", proto.ColumnType_INT, "Device state: -1 unlinked, 0 offline, 1 online."), column("enrolled_in_zdm", proto.ColumnType_BOOL, "Whether the device is enrolled in ZDM."), column("connected_to_zdm", proto.ColumnType_BOOL, "Whether the device is connected to ZDM."), column("last_online", proto.ColumnType_TIMESTAMP, "Last online time reported by Zoom."))},
		"zoom_room":          {description: "Zoom Rooms visible to the account.", columns: stringsColumns("id", "room_id", "name", "status", "location_id")},
		"zoom_room_device":   {description: "Devices associated with Zoom Rooms. With no room_id filter, enumerate every Room.", parentKey: "room_id", keys: optional("room_id"), columns: append(stringsColumns("id", "room_id", "room_name", "status", "app_version", "device_system", "device_type", "device_hostname", "device_manufacturer", "device_model", "device_firmware", "ip_address", "serial_number"), column("device_mac_addresses", proto.ColumnType_JSON, "Device MAC addresses reported by Zoom."))},
		"zoom_signin_log":    {description: "Sign-in and sign-out activity. Default: preceding 30 days in UTC. Optional time >= and < bounds define a half-open interval of at most 30 days. LIMIT does not reduce API requests.", logs: true, keys: plugin.KeyColumnSlice{{Name: "time", Require: plugin.Optional, Operators: []string{">=", "<", "=", ">", "<=", "<>"}}}, columns: append(stringsColumns("email", "type", "ip_address", "client_type", "version"), column("time", proto.ColumnType_TIMESTAMP, "Activity time in UTC."))},
		"zoom_operation_log": {description: "Administrative operation logs. Default: preceding 30 days in UTC. Optional time >= and < bounds define a half-open interval of at most 30 days. LIMIT does not reduce API requests.", logs: true, keys: plugin.KeyColumnSlice{{Name: "time", Require: plugin.Optional, Operators: []string{">=", "<", "=", ">", "<=", "<>"}}}, columns: append(stringsColumns("operator", "action", "category_type", "operation_detail"), column("time", proto.ColumnType_TIMESTAMP, "Operation time in UTC."))},
	}
}
