package main

import "errors"

func itemExtras(s State, u M, t, f string, line M, version int64) (M, error) {
	template := latest(s, f)
	if version > 0 {
		for _, v := range s["forms"] {
			if str(v, "id") == f && number(v, "version") == version {
				template = v
			}
		}
	}
	if template == nil {
		return nil, errors.New("Item template not found")
	}
	fields := []any{}
	data := M{}
	for k, v := range obj(line, "itemData") {
		data["item_"+k] = v
	}
	for _, raw := range arr(template, "itemFields") {
		field := raw.(map[string]any)
		copy := M{}
		for k, v := range field {
			copy[k] = v
		}
		copy["key"] = "item_" + str(field, "key")
		fields = append(fields, copy)
	}
	fake := State{}
	for k, v := range s {
		fake[k] = v
	}
	fake["forms"] = []M{{"id": f, "version": number(template, "version"), "fields": fields}}
	result, _, err := financialExtras(fake, u, t, M{"data": data}, f, nil)
	if err != nil {
		return nil, err
	}
	out := M{}
	for k, v := range result {
		out[k[5:]] = v
	}
	return out, nil
}
func redactItemExtras(s State, u M, t, f string, lines []any) {
	for _, raw := range lines {
		line, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		for k := range obj(line, "itemData") {
			if access(s, u, t, "item_"+k, f) == "hidden" {
				delete(obj(line, "itemData"), k)
			}
		}
	}
}
