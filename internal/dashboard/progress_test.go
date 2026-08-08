package dashboard

import (
	"math"
	"testing"
)

func TestMachineProgressParsing(t *testing.T) {
	parser := newProgressParser()
	lines := []string{
		"frame=240",
		"fps=12.0",
		"out_time_us=12500000",
		"speed=0.31x",
		"progress=continue",
	}
	var record ffmpegProgressRecord
	var complete bool
	for _, line := range lines {
		var machine bool
		record, complete, machine = parser.Consume(line)
		if !machine {
			t.Fatalf("%q was not recognized as a machine-progress line", line)
		}
	}
	if !complete || record.End || record.ProcessedSeconds != 12.5 || record.Speed != 0.31 {
		t.Fatalf("parsed record = %#v, complete %v", record, complete)
	}
}

func TestMachineProgressFallsBackToOutTimeMS(t *testing.T) {
	parser := newProgressParser()
	_, _, _ = parser.Consume("out_time_ms=2750000")
	record, complete, _ := parser.Consume("progress=continue")
	if !complete || record.ProcessedSeconds != 2.75 {
		t.Fatalf("out_time_ms parsed as %#v", record)
	}
}

func TestHEVCPassPercentagesAndWeighting(t *testing.T) {
	record := ffmpegProgressRecord{ProcessedSeconds: 25, Speed: 0.5}
	pass1 := progressFromRecord(stageHEVCPass1, 100, record)
	if pass1.StageProgressPercent != 25 || pass1.ProgressPercent != 12.5 {
		t.Fatalf("pass 1 progress = %#v; want stage 25 and overall 12.5", pass1)
	}
	pass2 := progressFromRecord(stageHEVCPass2, 100, record)
	if pass2.StageProgressPercent != 25 || pass2.ProgressPercent != 62.25 {
		t.Fatalf("pass 2 progress = %#v; want stage 25 and overall 62.25", pass2)
	}

	pass1End := progressFromRecord(stageHEVCPass1, 100, ffmpegProgressRecord{End: true})
	pass2End := progressFromRecord(stageHEVCPass2, 100, ffmpegProgressRecord{End: true})
	if pass1End.ProgressPercent != 50 || pass2End.ProgressPercent != 99 {
		t.Fatalf("weighted pass ends = %.2f, %.2f; want 50, 99", pass1End.ProgressPercent, pass2End.ProgressPercent)
	}
}

func TestAV1EncodingReservesValidationPercent(t *testing.T) {
	update := progressFromRecord(stageAV1, 100, ffmpegProgressRecord{ProcessedSeconds: 50, Speed: 0.8})
	if update.StageProgressPercent != 50 || update.ProgressPercent != 49.5 {
		t.Fatalf("AV1 progress = %#v; want stage 50 and overall 49.5", update)
	}
	end := progressFromRecord(stageAV1, 100, ffmpegProgressRecord{End: true})
	if end.ProgressPercent != 99 {
		t.Fatalf("AV1 end progress = %.2f; want 99", end.ProgressPercent)
	}
}

func TestETACalculation(t *testing.T) {
	pass1ETA, estimated := calculateETA(stageHEVCPass1, 100, 25, 0.5)
	if pass1ETA == nil || *pass1ETA != 350 || !estimated {
		t.Fatalf("pass 1 ETA = %v, estimated %v; want 350, true", pass1ETA, estimated)
	}
	pass2ETA, estimated := calculateETA(stageHEVCPass2, 100, 25, 0.5)
	if pass2ETA == nil || *pass2ETA != 150 || estimated {
		t.Fatalf("pass 2 ETA = %v, estimated %v; want 150, false", pass2ETA, estimated)
	}
	for _, test := range []struct{ duration, processed, speed float64 }{{0, 1, 1}, {100, 0, 1}, {100, 20, 0}, {100, 100, 1}} {
		if eta, _ := calculateETA(stageHEVCPass2, test.duration, test.processed, test.speed); eta != nil {
			t.Fatalf("insufficient data (%v) produced ETA %v", test, *eta)
		}
	}
}

func TestSpeedParsing(t *testing.T) {
	if got := parseEncodeSpeed(" 1.25x "); got != 1.25 {
		t.Fatalf("speed = %v; want 1.25", got)
	}
	for _, malformed := range []string{"", "N/A", "0x", "-1x", "NaNx", "+Infx"} {
		if got := parseEncodeSpeed(malformed); got != 0 {
			t.Fatalf("parseEncodeSpeed(%q) = %v; want 0", malformed, got)
		}
	}
}

func TestProgressEnd(t *testing.T) {
	parser := newProgressParser()
	_, _, _ = parser.Consume("out_time_us=99900000")
	_, _, _ = parser.Consume("speed=0.75x")
	record, complete, machine := parser.Consume("progress=end")
	if !machine || !complete || !record.End {
		t.Fatalf("progress=end parsed as %#v, complete %v, machine %v", record, complete, machine)
	}
	update := progressFromRecord(stageHEVCPass2, 100, record)
	if update.StageProgressPercent != 100 || update.ProgressPercent != 99 || update.ProcessedSeconds != 100 || update.ETASeconds != nil {
		t.Fatalf("end update = %#v", update)
	}
}

func TestMalformedProgressData(t *testing.T) {
	parser := newProgressParser()
	if _, _, machine := parser.Consume("ordinary log line"); machine {
		t.Fatal("ordinary log line was treated as machine progress")
	}
	_, _, _ = parser.Consume("out_time_us=not-a-number")
	_, _, _ = parser.Consume("speed=very-fast")
	record, complete, _ := parser.Consume("progress=continue")
	if !complete || record.ProcessedSeconds != 0 || record.Speed != 0 || math.IsNaN(record.Speed) {
		t.Fatalf("malformed data produced %#v", record)
	}
	if _, complete, machine := parser.Consume("progress=broken"); !machine || complete {
		t.Fatalf("malformed progress marker = complete %v, machine %v", complete, machine)
	}
}

func TestRepeatedMempolicyWarningsAreFilteredExactly(t *testing.T) {
	for i := 0; i < 500; i++ {
		if !isRepetitiveMempolicyWarning("set_mempolicy: Operation not permitted") {
			t.Fatal("exact repetitive warning was not filtered")
		}
	}
	for _, meaningful := range []string{
		"set_mempolicy: a different error",
		"x265 [error]: encoding failed",
		"ffmpeg: Operation not permitted",
	} {
		if isRepetitiveMempolicyWarning(meaningful) {
			t.Fatalf("meaningful line %q was filtered", meaningful)
		}
	}
}
