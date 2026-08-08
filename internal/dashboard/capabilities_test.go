package dashboard

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCLICapabilityDetectionRequiresRuntimeVerifiedResponse(t *testing.T) {
	script := filepath.Join(t.TempDir(), "shrinkray")
	contents := `#!/usr/bin/env bash
set -euo pipefail
[ "${1-}" = capabilities ] && [ "${2-}" = --json ]
printf '{"encoders":{"software":true,"qsv":false,"vaapi":true,"nvenc":false},"auto_selected":"vaapi","devices":{"qsv":"","vaapi":"/dev/dri/renderD129"}}\n'
`
	if err := os.WriteFile(script, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
	capabilities, err := detectEncoderCapabilities(context.Background(), script)
	if err != nil {
		t.Fatal(err)
	}
	if !capabilities.Encoders.Software || capabilities.Encoders.QSV || !capabilities.Encoders.VAAPI || capabilities.AutoSelected != "vaapi" {
		t.Fatalf("capabilities = %#v", capabilities)
	}
}

func TestCapabilityDetectionRejectsUnknownAutoBackend(t *testing.T) {
	script := filepath.Join(t.TempDir(), "shrinkray")
	if err := os.WriteFile(script, []byte("#!/usr/bin/env bash\nprintf '%s\\n' '{\"encoders\":{},\"auto_selected\":\"mystery\"}'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := detectEncoderCapabilities(context.Background(), script); err == nil {
		t.Fatal("unknown selected backend was accepted")
	}
}

func TestHardwareStagesUseSinglePassProgressAndETA(t *testing.T) {
	for _, stage := range []string{stageHardwareQSV, stageHardwareVAAPI, stageHardwareNVENC} {
		update := progressFromRecord(stage, 100, ffmpegProgressRecord{ProcessedSeconds: 25, Speed: 0.5})
		if update.ProgressPercent != 24.75 || update.StageProgressPercent != 25 || update.ETASeconds == nil || *update.ETASeconds != 150 || update.ETAIsEstimate {
			t.Fatalf("hardware progress for %q = %#v", stage, update)
		}
	}
}

func TestHardwareStageIdentifiesActualBackend(t *testing.T) {
	tests := map[string]string{
		"==> Encoding with HEVC hardware — Intel QSV...":    "qsv",
		"==> Encoding with HEVC hardware — VAAPI...":        "vaapi",
		"==> Encoding with HEVC hardware — NVIDIA NVENC...": "nvenc",
	}
	for line, want := range tests {
		stage := stageFromLog(line)
		if actual := actualEncoderFromStage(stage); actual != want {
			t.Errorf("stage/actual for %q = %q/%q; want %q", line, stage, actual, want)
		}
		if stage == stageHEVCPass1 || stage == stageHEVCPass2 {
			t.Errorf("hardware line %q claimed a software two-pass stage", line)
		}
	}
}
