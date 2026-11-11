package directadmin

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ChangeSet is an ExternalDNS plan. Delete is applied before Create.
type ChangeSet struct {
	Create []Endpoint
	Delete []Endpoint
}

// DecodeChanges accepts the v0.21 body (create/delete/updateOld/updateNew)
// and the older capitalized keys. An update is a delete of the old target
// followed by a create of the new one.
func DecodeChanges(raw []byte) (ChangeSet, error) {
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		return ChangeSet{}, err
	}
	toDelete, err := firstList(body, "delete", "Delete")
	if err != nil {
		return ChangeSet{}, err
	}
	old, err := firstList(body, "updateOld", "UpdateOld")
	if err != nil {
		return ChangeSet{}, err
	}
	toCreate, err := firstList(body, "create", "Create")
	if err != nil {
		return ChangeSet{}, err
	}
	newRecords, err := firstList(body, "updateNew", "UpdateNew")
	if err != nil {
		return ChangeSet{}, err
	}
	return ChangeSet{Create: append(toCreate, newRecords...), Delete: append(toDelete, old...)}, nil
}

func firstList(body map[string]json.RawMessage, keys ...string) ([]Endpoint, error) {
	for _, key := range keys {
		raw, ok := body[key]
		if !ok || len(raw) == 0 || string(raw) == "null" {
			continue
		}
		var items []Endpoint
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		if len(items) > 0 {
			return items, nil
		}
	}
	return nil, nil
}

func jsonUnmarshal(body string, dest any) error {
	return json.Unmarshal([]byte(body), dest)
}

// AdjustEndpoints canonicalizes names and CNAME targets before ExternalDNS
// diffs them against Records. Other fields are preserved.
func AdjustEndpoints(raw []byte) ([]byte, error) {
	var endpoints []map[string]any
	if err := json.Unmarshal(raw, &endpoints); err != nil {
		return nil, err
	}
	for _, ep := range endpoints {
		recordType := strings.ToUpper(asString(ep["recordType"]))
		if name := asString(ep["dnsName"]); name != "" {
			ep["dnsName"] = strings.ToLower(strings.TrimSuffix(name, "."))
		}
		targets, ok := ep["targets"].([]any)
		if !ok {
			continue
		}
		for i, target := range targets {
			targets[i] = canonicalTarget(recordType, asString(target))
		}
	}
	return json.Marshal(endpoints)
}
