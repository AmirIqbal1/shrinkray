package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"time"
)

type EncoderAvailability struct {
	Software bool `json:"software"`
	QSV      bool `json:"qsv"`
	VAAPI    bool `json:"vaapi"`
	NVENC    bool `json:"nvenc"`
}

type EncoderCapabilities struct {
	Encoders     EncoderAvailability `json:"encoders"`
	AutoSelected string              `json:"auto_selected"`
}

func detectEncoderCapabilities(ctx context.Context, shrinkrayBin string) (EncoderCapabilities, error) {
	detectCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	output, err := exec.CommandContext(detectCtx, shrinkrayBin, "capabilities", "--json").Output()
	if err != nil {
		return EncoderCapabilities{}, errors.New("could not detect encoder capabilities")
	}
	var wire struct {
		Encoders     EncoderAvailability `json:"encoders"`
		AutoSelected string              `json:"auto_selected"`
		Devices      map[string]string   `json:"devices"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(output)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return EncoderCapabilities{}, errors.New("encoder capability response was invalid")
	}
	if !validActualEncoder(wire.AutoSelected) && wire.AutoSelected != "" {
		return EncoderCapabilities{}, errors.New("encoder capability response selected an unknown backend")
	}
	return EncoderCapabilities{Encoders: wire.Encoders, AutoSelected: wire.AutoSelected}, nil
}

func validRequestedEncoder(encoder string) bool {
	switch encoder {
	case "auto", "software", "qsv", "vaapi", "nvenc":
		return true
	default:
		return false
	}
}

func validActualEncoder(encoder string) bool {
	switch encoder {
	case "software", "qsv", "vaapi", "nvenc":
		return true
	default:
		return false
	}
}
