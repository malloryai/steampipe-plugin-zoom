package zoom

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func instant(s string) time.Time {
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return v
}
func scanRows(c *client, name string, opts scanOptions) ([]row, error) {
	var rows []row
	err := c.scan(context.Background(), name, opts, func(r row) error { rows = append(rows, r); return nil })
	return rows, err
}
func TestCompleteUsersAndDevicePartitions(t *testing.T) {
	var requests []string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			tokenResponse(w)
			return
		}
		q := r.URL.Query()
		if r.URL.Path == "/users" {
			requests = append(requests, q.Get("status"))
			fmt.Fprintf(w, `{"users":[{"id":%q}]}`, q.Get("status"))
			return
		}
		requests = append(requests, q.Get("device_type")+":"+q.Get("is_enrolled_in_zdm")+":"+q.Get("device_status"))
		if q.Get("next_page_token") == "" {
			fmt.Fprint(w, `{"devices":[{"device_id":"first"}],"next_page_token":"second"}`)
		} else {
			fmt.Fprint(w, `{"devices":[{"device_id":"last"}]}`)
		}
	})
	rows, err := scanRows(c, "zoom_user", scanOptions{})
	if err != nil || len(rows) != 3 || strings.Join(requests, ",") != "active,inactive,pending" {
		t.Fatalf("users %v %v", requests, err)
	}
	requests = nil
	rows, err = scanRows(c, "zoom_device", scanOptions{})
	if err != nil || len(rows) != 60 {
		t.Fatalf("devices count %d %v", len(rows), err)
	}
	seen := map[string]int{}
	for _, r := range requests {
		seen[r]++
	}
	if len(seen) != 30 {
		t.Fatal(seen)
	}
	for _, typ := range []string{"-1", "5", "7", "8", "9"} {
		for _, enrolled := range []string{"true", "false"} {
			for _, status := range []string{"-1", "0", "1"} {
				if seen[typ+":"+enrolled+":"+status] != 2 {
					t.Fatal(seen)
				}
			}
		}
	}
}
func TestFanoutAndDeniedChild(t *testing.T) {
	for _, table := range []string{"zoom_group_member", "zoom_room_device"} {
		t.Run(table, func(t *testing.T) {
			parent, child, key := "groups", "members", "group_id"
			if table == "zoom_room_device" {
				parent, child, key = "rooms", "devices", "room_id"
			}
			var paths []string
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/oauth/token" {
					tokenResponse(w)
					return
				}
				paths = append(paths, r.URL.Path)
				if r.URL.Path == "/"+parent {
					fmt.Fprintf(w, `{%q:[{"id":"a"},{"id":"b"}]}`, parent)
					return
				}
				if strings.Contains(r.URL.Path, "/b/") {
					w.WriteHeader(403)
					return
				}
				fmt.Fprintf(w, `{%q:[{"id":"c"}]}`, child)
			})
			rows, err := scanRows(c, table, scanOptions{parentID: "a"})
			if err != nil || len(rows) != 1 || rows[0][key] != "a" || !reflect.DeepEqual(paths, []string{"/" + parent + "/a/" + child}) {
				t.Fatalf("qualified child %v %v %v", rows, paths, err)
			}
			if _, err = scanRows(c, table, scanOptions{}); err == nil {
				t.Fatal("denied child ignored")
			}
		})
	}
}
func TestDateWindowsAndHalfOpenFilter(t *testing.T) {
	start, end := instant("2026-01-28T12:00:00Z"), instant("2026-02-08T00:00:00Z")
	windows := dateWindows(start, end)
	want := []dateWindow{{"2026-01-28", "2026-01-31"}, {"2026-02-01", "2026-02-07"}}
	if !reflect.DeepEqual(windows, want) {
		t.Fatalf("windows %#v", windows)
	}
	var requests []string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			tokenResponse(w)
			return
		}
		requests = append(requests, r.URL.Query().Get("from")+"/"+r.URL.Query().Get("to"))
		fmt.Fprint(w, `{"activity_logs":[{"time":"2026-01-28T11:59:59Z"},{"time":"2026-01-28T12:00:00Z"},{"time":"2026-02-07T23:59:59Z"},{"time":"2026-02-08T00:00:00Z"}]}`)
	})
	rows, err := scanRows(c, "zoom_signin_log", scanOptions{start: start, end: end})
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows %v err %v", rows, err)
	}
	if !reflect.DeepEqual(requests, []string{"2026-01-28/2026-01-31", "2026-02-01/2026-02-07"}) {
		t.Fatal(requests)
	}
}
func TestBounds(t *testing.T) {
	now := instant("2026-06-02T12:00:00Z")
	start, end, err := timeBounds(now, nil)
	if err != nil || !end.Equal(now) || !start.Equal(instant("2026-05-03T12:00:00Z")) {
		t.Fatalf("default %v %v %v", start, end, err)
	}
	_, _, err = timeBounds(now, []timeBound{{operator: ">=", value: now.Add(-31 * 24 * time.Hour)}})
	if err == nil {
		t.Fatal("accepted >30 days")
	}
	start, end, err = timeBounds(now, []timeBound{{operator: "<", value: instant("2026-05-01T00:00:00Z")}})
	if err != nil || !start.Equal(instant("2026-04-01T00:00:00Z")) || !end.Equal(instant("2026-05-01T00:00:00Z")) {
		t.Fatalf("upper only %v %v %v", start, end, err)
	}
}
func TestStagingDiscardsPartialFailure(t *testing.T) {
	emitted := 0
	err := stageRows(context.Background(), func(emit func(row) error) error {
		if err := emit(row{"id": "first"}); err != nil {
			return err
		}
		return fmt.Errorf("denied child")
	}, func(row) error { emitted++; return nil })
	if err == nil || emitted != 0 {
		t.Fatalf("partial rows escaped: %d %v", emitted, err)
	}
	err = stageRows(context.Background(), func(emit func(row) error) error { return emit(row{"id": "complete", "device_status": float64(0)}) }, func(r row) error {
		emitted++
		if r["device_status"] != float64(0) {
			t.Fatal(r)
		}
		return nil
	})
	if err != nil || emitted != 1 {
		t.Fatalf("complete rows %d %v", emitted, err)
	}
}

func TestStagingUsesPrivateStorageAndRemovesIt(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("TMPDIR", directory)
	err := stageRows(context.Background(), func(emit func(row) error) error {
		entries, err := os.ReadDir(directory)
		if err != nil {
			return err
		}
		if len(entries) != 1 {
			t.Fatalf("expected one scan file, got %d", len(entries))
		}
		info, err := entries[0].Info()
		if err != nil {
			return err
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("scan permissions %o", info.Mode().Perm())
		}
		return emit(row{"id": "private"})
	}, func(row) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("scan file retained: %v %v", entries, err)
	}
}

func TestRoomsIncludeUnassignedAndDeduplicateParents(t *testing.T) {
	var childPaths []string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			tokenResponse(w)
			return
		}
		if r.URL.Path == "/rooms" {
			if r.URL.Query().Get("unassigned_rooms") == "true" {
				fmt.Fprint(w, `{"rooms":[{"id":"shared"},{"id":"unassigned"}]}`)
			} else {
				fmt.Fprint(w, `{"rooms":[{"id":"assigned"},{"id":"shared"}]}`)
			}
			return
		}
		childPaths = append(childPaths, r.URL.Path)
		fmt.Fprint(w, `{"devices":[{"id":"device"}]}`)
	})
	rows, err := scanRows(c, "zoom_room", scanOptions{})
	if err != nil || len(rows) != 3 {
		t.Fatalf("room populations: %d %v", len(rows), err)
	}
	rows, err = scanRows(c, "zoom_room_device", scanOptions{})
	if err != nil || len(rows) != 3 {
		t.Fatalf("room device populations: %d %v", len(rows), err)
	}
	if !reflect.DeepEqual(childPaths, []string{"/rooms/assigned/devices", "/rooms/shared/devices", "/rooms/unassigned/devices"}) {
		t.Fatal(childPaths)
	}
}

func TestFutureUpperBoundAndTightestBounds(t *testing.T) {
	now := instant("2026-06-02T12:00:00Z")
	upper := now.Add(24 * time.Hour)
	for _, bounds := range [][]timeBound{
		{{operator: "<", value: upper}},
		{{operator: "<", value: upper.Add(time.Hour)}, {operator: "<", value: upper}},
		{{operator: "<", value: upper}, {operator: "<", value: upper.Add(time.Hour)}},
	} {
		start, end, err := timeBounds(now, bounds)
		if err != nil || !end.Equal(upper) || !start.Equal(upper.Add(-30*24*time.Hour)) {
			t.Fatalf("future upper bound: %v %v %v", start, end, err)
		}
	}
}
