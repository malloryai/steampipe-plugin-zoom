package zoom

import (
	"context"
	"testing"

	"github.com/turbot/steampipe-plugin-sdk/v5/connection"
	"github.com/turbot/steampipe-plugin-sdk/v5/plugin"
)

func TestCredentialRotationUsesNewClient(t *testing.T) {
	ctx := context.Background()
	cache, err := connection.NewConnectionCache("test", 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config()
	d := &plugin.QueryData{Connection: &plugin.Connection{Name: "test", Config: cfg}, ConnectionCache: cache}
	first, err := connect(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	again, err := connect(ctx, d)
	if err != nil || first != again {
		t.Fatal("client not reused within credential revision")
	}
	cfg.ClientSecret = ptr("rotated")
	d.Connection.Config = cfg
	rotated, err := connect(ctx, d)
	if err != nil || first == rotated {
		t.Fatal("credential revision reused old client")
	}
}

func TestPluginInstancesOwnTheirColumnDefinitions(t *testing.T) {
	first, second := Plugin(context.Background()), Plugin(context.Background())
	first.TableMap["zoom_user"].Columns[0].Description = "changed by SDK initialization"
	if second.TableMap["zoom_user"].Columns[0].Description == "changed by SDK initialization" {
		t.Fatal("plugin instances share mutable schema definitions")
	}
}

func TestScopedTableSchema(t *testing.T) {
	p := Plugin(context.Background())
	want := map[string][]string{
		"zoom_user": {"id", "email", "status"}, "zoom_group": {"id", "name"}, "zoom_group_member": {"group_id", "id", "email"}, "zoom_device": {"device_id", "device_status", "enrolled_in_zdm", "model", "vendor"}, "zoom_room": {"id", "location_id"}, "zoom_room_device": {"room_id", "id", "status", "app_version"}, "zoom_signin_log": {"time", "email", "type", "ip_address"}, "zoom_operation_log": {"time", "operator", "operation_detail"},
	}
	if len(p.TableMap) != len(want) {
		t.Fatal("unexpected table surface")
	}
	for name, columns := range want {
		table, ok := p.TableMap[name]
		if !ok {
			t.Fatalf("missing %s", name)
		}
		schema, err := table.GetSchema()
		if err != nil {
			t.Fatal(err)
		}
		found := map[string]bool{}
		for _, col := range schema.Columns {
			found[col.Name] = true
		}
		for _, column := range columns {
			if !found[column] {
				t.Fatalf("missing %s.%s", name, column)
			}
		}
		for _, sensitive := range []string{"account_id", "host_key", "client_secret", "access_token"} {
			if found[sensitive] {
				t.Fatalf("sensitive column %s.%s", name, sensitive)
			}
		}
	}
}
