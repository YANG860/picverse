package utils

import (
	"encoding/json"

	"gorm.io/datatypes"
)

func UnmarshalJSON(data []byte) (datatypes.JSONMap, error) {
	var v = datatypes.JSONMap{}

	if len(data) == 0 {
		return v, nil
	}

	err := json.Unmarshal(data, &v)
	return v, err
}
