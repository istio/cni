package server

import (
	"encoding/json"
)

func toDebugJSON(obj interface{}) string {
	b, err := toJSON(obj)
	if err != nil {
		b = "error marshalling to JSON: " + err.Error()
	}
	return b
}

func toJSON(obj interface{}) (string, error) {
	bytes, err := json.Marshal(obj)
	if err != nil {
		return "", err
	}
	return string(bytes), nil
}
