package dashboard

import (
	"math"
	"strconv"
	"strings"
)

const (
	stageHEVCPass1        = "HEVC pass 1 of 2"
	stageHEVCPass2        = "HEVC pass 2 of 2"
	stageAV1              = "AV1 encoding"
	stageHardwareQSV      = "HEVC hardware encode — Intel QSV"
	stageHardwareVAAPI    = "HEVC hardware encode — VAAPI"
	stageHardwareNVENC    = "HEVC hardware encode — NVIDIA NVENC"
	stageValidation       = "Validating output"
	stageInsufficientDisk = "Insufficient disk space"
	stageCriticalDisk     = "Critical low disk space"
)

type ProgressUpdate struct {
	ProgressPercent      float64
	StageProgressPercent float64
	DurationSeconds      float64
	ProcessedSeconds     float64
	ETASeconds           *float64
	EncodeSpeed          float64
	ETAIsEstimate        bool
}

type ffmpegProgressRecord struct {
	ProcessedSeconds float64
	Speed            float64
	End              bool
}

type progressParser struct {
	values map[string]string
}

func newProgressParser() *progressParser {
	return &progressParser{values: make(map[string]string)}
}

// Consume recognizes FFmpeg's -progress key/value protocol. The final return
// value reports whether the line belongs to the protocol and should therefore
// be kept out of the human-readable dashboard log.
func (p *progressParser) Consume(line string) (ffmpegProgressRecord, bool, bool) {
	line = strings.TrimSpace(line)
	key, value, found := strings.Cut(line, "=")
	if !found || !machineProgressKey(key) {
		return ffmpegProgressRecord{}, false, false
	}
	p.values[key] = value
	if key != "progress" {
		return ffmpegProgressRecord{}, false, true
	}

	values := p.values
	p.values = make(map[string]string)
	if value != "continue" && value != "end" {
		return ffmpegProgressRecord{}, false, true
	}
	record := ffmpegProgressRecord{End: value == "end"}
	if raw := values["out_time_us"]; raw != "" {
		record.ProcessedSeconds = parseFFmpegTimestamp(raw)
	} else if raw := values["out_time_ms"]; raw != "" {
		// Despite its historical name, FFmpeg reports out_time_ms in
		// microseconds, matching out_time_us in newer versions.
		record.ProcessedSeconds = parseFFmpegTimestamp(raw)
	}
	record.Speed = parseEncodeSpeed(values["speed"])
	return record, true, true
}

func machineProgressKey(key string) bool {
	switch key {
	case "frame", "fps", "bitrate", "total_size", "out_time_us", "out_time_ms", "out_time", "dup_frames", "drop_frames", "speed", "progress":
		return true
	default:
		return strings.HasPrefix(key, "stream_")
	}
}

func parseFFmpegTimestamp(value string) float64 {
	microseconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || microseconds < 0 {
		return 0
	}
	return float64(microseconds) / 1_000_000
}

func parseEncodeSpeed(value string) float64 {
	value = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(value), "x"))
	speed, err := strconv.ParseFloat(value, 64)
	if err != nil || speed <= 0 || math.IsInf(speed, 0) || math.IsNaN(speed) {
		return 0
	}
	return speed
}

func progressFromRecord(stage string, duration float64, record ffmpegProgressRecord) ProgressUpdate {
	processed := clamp(record.ProcessedSeconds, 0, duration)
	stagePercent := 0.0
	if duration > 0 {
		stagePercent = clamp(processed/duration*100, 0, 100)
	}
	if record.End {
		stagePercent = 100
		processed = duration
	}

	overall := stagePercent * 0.99
	switch stage {
	case stageHEVCPass1:
		overall = stagePercent * 0.5
	case stageHEVCPass2:
		overall = 50 + stagePercent*0.49
	}

	eta, estimate := calculateETA(stage, duration, processed, record.Speed)
	return ProgressUpdate{
		ProgressPercent:      clamp(overall, 0, 99),
		StageProgressPercent: stagePercent,
		DurationSeconds:      duration,
		ProcessedSeconds:     processed,
		ETASeconds:           eta,
		EncodeSpeed:          record.Speed,
		ETAIsEstimate:        estimate,
	}
}

func calculateETA(stage string, duration, processed, speed float64) (*float64, bool) {
	if duration <= 0 || processed <= 0 || speed <= 0 || processed >= duration {
		return nil, false
	}
	remaining := duration - processed
	estimate := false
	if stage == stageHEVCPass1 {
		remaining += duration
		estimate = true
	}
	seconds := remaining / speed
	if seconds < 0 || math.IsInf(seconds, 0) || math.IsNaN(seconds) {
		return nil, false
	}
	return &seconds, estimate
}

func clamp(value, minimum, maximum float64) float64 {
	if maximum < minimum {
		return minimum
	}
	return math.Min(maximum, math.Max(minimum, value))
}

func isRepetitiveMempolicyWarning(line string) bool {
	return strings.TrimSpace(line) == "set_mempolicy: Operation not permitted"
}
