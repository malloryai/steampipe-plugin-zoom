package zoom

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"time"
)

type scanOptions struct {
	parentID, status string
	start, end       time.Time
}
type timeBound struct {
	operator string
	value    time.Time
}
type dateWindow struct{ from, to string }

func timeBounds(now time.Time, bounds []timeBound) (time.Time, time.Time, error) {
	end := now.UTC()
	var start time.Time
	hasEnd := false
	for _, bound := range bounds {
		value := bound.value.UTC()
		switch bound.operator {
		case ">=":
			if start.IsZero() || value.After(start) {
				start = value
			}
		case "<":
			if !hasEnd || value.Before(end) {
				end = value
				hasEnd = true
			}
		default:
			return time.Time{}, time.Time{}, errors.New("Zoom log time bounds support >= and <")
		}
	}
	if start.IsZero() {
		start = end.Add(-30 * 24 * time.Hour)
	}
	if end.Sub(start) > 30*24*time.Hour {
		return start, end, errors.New("Zoom log queries cannot exceed 30 days")
	}
	return start, end, nil
}

func dateWindows(start, end time.Time) []dateWindow {
	var result []dateWindow
	if !start.Before(end) {
		return result
	}
	day := time.Date(start.UTC().Year(), start.UTC().Month(), start.UTC().Day(), 0, 0, 0, 0, time.UTC)
	last := end.UTC().Add(-time.Nanosecond)
	for !day.After(last) {
		next := day.AddDate(0, 0, 7)
		month := time.Date(day.Year(), day.Month()+1, 1, 0, 0, 0, 0, time.UTC)
		if next.After(month) {
			next = month
		}
		to := next.Add(-time.Nanosecond)
		if to.After(last) {
			to = last
		}
		result = append(result, dateWindow{day.Format(time.DateOnly), to.Format(time.DateOnly)})
		day = next
	}
	return result
}

func (c *client) scan(ctx context.Context, table string, opts scanOptions, emit func(row) error) error {
	switch table {
	case "zoom_user":
		statuses := []string{"active", "inactive", "pending"}
		if opts.status != "" {
			switch opts.status {
			case "active", "inactive", "pending":
				statuses = []string{opts.status}
			default:
				return nil
			}
		}
		for _, status := range statuses {
			if err := c.pages(ctx, "/users", "users", url.Values{"status": {status}}, func(r row) error { r["status"] = status; return emit(r) }); err != nil {
				return err
			}
		}
		return nil
	case "zoom_group":
		return c.pages(ctx, "/groups", "groups", nil, emit)
	case "zoom_room":
		return c.rooms(ctx, emit)
	case "zoom_device":
		for _, kind := range []string{"-1", "5", "7", "8", "9"} {
			for _, enrolled := range []string{"true", "false"} {
				for _, status := range []string{"-1", "0", "1"} {
					if err := c.pages(ctx, "/devices", "devices", url.Values{"device_type": {kind}, "is_enrolled_in_zdm": {enrolled}, "device_status": {status}}, emit); err != nil {
						return err
					}
				}
			}
		}
		return nil
	case "zoom_group_member", "zoom_room_device":
		parent, child, key := "groups", "members", "group_id"
		if table == "zoom_room_device" {
			parent, child, key = "rooms", "devices", "room_id"
		}
		children := func(id string) error {
			return c.pages(ctx, "/"+parent+"/"+url.PathEscape(id)+"/"+child, child, nil, func(r row) error { r[key] = id; return emit(r) })
		}
		if opts.parentID != "" {
			return children(opts.parentID)
		}
		visitParent := func(r row) error {
			id, ok := r["id"].(string)
			if !ok || id == "" {
				return errors.New("Zoom parent row is missing its identifier")
			}
			return children(id)
		}
		if parent == "rooms" {
			return c.rooms(ctx, visitParent)
		}
		return c.pages(ctx, "/"+parent, parent, nil, visitParent)
	case "zoom_signin_log", "zoom_operation_log":
		path, key := "/report/activities", "activity_logs"
		if table == "zoom_operation_log" {
			path, key = "/report/operationlogs", "operation_logs"
		}
		for _, window := range dateWindows(opts.start, opts.end) {
			// Only accept the current provider window so unexpected overlapping pages cannot duplicate rows across windows.
			lower, _ := time.Parse(time.DateOnly, window.from)
			upper, _ := time.Parse(time.DateOnly, window.to)
			upper = upper.AddDate(0, 0, 1)
			if err := c.pages(ctx, path, key, url.Values{"from": {window.from}, "to": {window.to}}, func(r row) error {
				raw, ok := r["time"].(string)
				if !ok {
					return errors.New("Zoom log row is missing its timestamp")
				}
				timestamp, err := time.Parse(time.RFC3339Nano, raw)
				if err != nil {
					return errors.New("Zoom log row has an invalid timestamp")
				}
				if timestamp.Before(opts.start) || !timestamp.Before(opts.end) || timestamp.Before(lower) || !timestamp.Before(upper) {
					return nil
				}
				return emit(r)
			}); err != nil {
				return err
			}
		}
		return nil
	default:
		return errors.New("unsupported Zoom table")
	}
}

func (c *client) rooms(ctx context.Context, emit func(row) error) error {
	seen := map[string]bool{}
	for _, unassigned := range []string{"false", "true"} {
		if err := c.pages(ctx, "/rooms", "rooms", url.Values{"unassigned_rooms": {unassigned}}, func(item row) error {
			id, ok := item["id"].(string)
			if !ok || id == "" {
				return errors.New("Zoom Room row is missing its identifier")
			}
			if seen[id] {
				return nil
			}
			seen[id] = true
			return emit(item)
		}); err != nil {
			return err
		}
	}
	return nil
}

// stageRows completes the scan before exposing rows. Private disk storage bounds memory
// and prevents a failed later page from producing a partial snapshot.
func stageRows(ctx context.Context, scan func(func(row) error) error, emit func(row) error) error {
	file, err := os.CreateTemp("", "zoom-scan-*.ndjson")
	if err != nil {
		return errors.New("Zoom scan storage could not be created")
	}
	defer os.Remove(file.Name())
	defer file.Close()
	encoder := json.NewEncoder(file)
	if err := scan(func(r row) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := encoder.Encode(r); err != nil {
			return errors.New("Zoom scan storage could not be written")
		}
		return nil
	}); err != nil {
		return err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return errors.New("Zoom scan storage could not be read")
	}
	decoder := json.NewDecoder(file)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var item row
		err := decoder.Decode(&item)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return errors.New("Zoom scan storage could not be decoded")
		}
		if err := emit(item); err != nil {
			return err
		}
	}
}
