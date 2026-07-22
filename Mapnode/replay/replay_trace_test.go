package replay

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTracePreparationMatchesLegacySortAndNormalization(t *testing.T) {
	fixture := filepath.Join("..", "..", "tests", "fixtures", "replay", "msg-small.csv")
	raw, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	wantDigestBytes := sha256.Sum256(raw)
	wantDigest := hex.EncodeToString(wantDigestBytes[:])

	trace, err := PrepareTrace(fixture, TraceOptions{InvalidRowPolicy: InvalidRowReject})
	if err != nil {
		t.Fatalf("PrepareTrace() error = %v", err)
	}
	if trace.Digest != wantDigest {
		t.Fatalf("digest = %q, want %q", trace.Digest, wantDigest)
	}
	if trace.SourceRows != 5 || trace.DroppedRows != 0 || len(trace.Events) != 5 {
		t.Fatalf("row counts = source %d dropped %d prepared %d", trace.SourceRows, trace.DroppedRows, len(trace.Events))
	}
	wantChains := []string{"alpha", "beta", "gamma"}
	if !equalStrings(trace.Chains, wantChains) {
		t.Fatalf("chains = %#v, want %#v", trace.Chains, wantChains)
	}
	wantInitial := map[string]uint64{"alpha": 100, "beta": 200, "gamma": 50}
	for chain, want := range wantInitial {
		if got := trace.InitHeights[chain]; got != want {
			t.Errorf("init height %s = %d, want %d", chain, got, want)
		}
	}

	// The first two rows have equal legacy sort keys. OriginalRow is the final
	// stable tie-breaker and Sequence is assigned only after sorting.
	wantOriginalRows := []uint64{3, 4, 2, 1, 5}
	for index, want := range wantOriginalRows {
		event := trace.Events[index]
		if event.Sequence != uint64(index) || event.OriginalRow != want {
			t.Errorf("event %d identity = sequence %d original row %d, want %d/%d", index, event.Sequence, event.OriginalRow, index, want)
		}
		if event.ID == "" {
			t.Errorf("event %d has empty identity", index)
		}
	}
	if trace.Events[0].Source.Chain != "gamma" || trace.Events[0].Destination.Chain != "alpha" {
		t.Fatalf("canonical chains not applied: %#v", trace.Events[0])
	}
	if trace.Events[0].Source.OriginalHeight != 50 || trace.Events[0].Source.NormalizedHeight != 0 {
		t.Fatalf("source block = %#v", trace.Events[0].Source)
	}
	if trace.Events[2].Source.OriginalHeight != 55 || trace.Events[2].Source.NormalizedHeight != 5 {
		t.Fatalf("sparse height was rank-compressed: %#v", trace.Events[2].Source)
	}
	if trace.Events[2].Destination.OriginalHeight != 105 || trace.Events[2].Destination.NormalizedHeight != 5 {
		t.Fatalf("destination normalization = %#v", trace.Events[2].Destination)
	}
	if !trace.Events[0].SourceTime.Equal(time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC)) ||
		!trace.Events[2].SourceTime.Equal(time.Date(2025, 12, 1, 0, 0, 1, 0, time.UTC)) {
		t.Fatalf("UTC time variants not canonicalized: %s / %s", trace.Events[0].SourceTime, trace.Events[2].SourceTime)
	}
	if trace.Events[3].BridgeName != "Bridge Z" || trace.Events[3].TxCount != 2 || trace.Events[3].VolumeUSD != 4.5 {
		t.Fatalf("metadata = %#v", trace.Events[3])
	}
	if trace.Events[1].TxCount != 0 || trace.Events[1].VolumeUSD != 0 {
		t.Fatalf("present invalid numeric metadata must use legacy zero defaults: %#v", trace.Events[1])
	}
}

func TestTraceHeightParsingIsExactAndUsesLegacyHalfEvenRounding(t *testing.T) {
	for input, want := range map[string]uint64{
		"9007199254740993":     9_007_199_254_740_993,
		"18446744073709551615": math.MaxUint64,
		"0.5":                  0,
		"1.5":                  2,
		"2.5":                  2,
		"1.25e2":               125,
	} {
		got, err := parseLegacyHeight(input)
		if err != nil || got != want {
			t.Errorf("parseLegacyHeight(%q) = %d, %v; want %d", input, got, err, want)
		}
	}
	for _, input := range []string{"-1", "18446744073709551616", "NaN", "Inf"} {
		if _, err := parseLegacyHeight(input); err == nil {
			t.Errorf("parseLegacyHeight(%q) accepted", input)
		}
	}
}

func TestTracePreparationFiltersBeforeLimitAndInitHeights(t *testing.T) {
	fixture := filepath.Join("..", "..", "tests", "fixtures", "replay", "msg-small.csv")
	trace, err := PrepareTrace(fixture, TraceOptions{
		InvalidRowPolicy: InvalidRowReject,
		ChainAllowlist:   []string{" ALPHA ", "GAMMA"},
		MaxEvents:        2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(trace.Events) != 2 {
		t.Fatalf("events = %d, want 2", len(trace.Events))
	}
	if got := trace.InitHeights["alpha"]; got != 100 {
		t.Fatalf("alpha initial height = %d, want 100", got)
	}
	if got := trace.InitHeights["gamma"]; got != 50 {
		t.Fatalf("gamma initial height = %d, want 50", got)
	}
	if len(trace.Chains) != 2 {
		t.Fatalf("chains = %#v", trace.Chains)
	}
}

func TestTraceRejectsMissingColumnsAndSupportsExplicitDropPolicy(t *testing.T) {
	directory := t.TempDir()
	missing := filepath.Join(directory, "missing.csv")
	if err := os.WriteFile(missing, []byte("src_chain,dst_chain\na,b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareTrace(missing, TraceOptions{InvalidRowPolicy: InvalidRowReject}); err == nil {
		t.Fatal("missing required columns accepted")
	}

	invalid := filepath.Join(directory, "invalid.csv")
	data := "src_chain,dst_chain,src_block_number,dst_block_number,src_block_time\n" +
		"a,b,10,20,2025-12-01 00:00:00 UTC\n" +
		"a,b,-1,21,not-a-time\n"
	if err := os.WriteFile(invalid, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareTrace(invalid, TraceOptions{InvalidRowPolicy: InvalidRowReject}); err == nil {
		t.Fatal("reject policy accepted invalid row")
	}
	trace, err := PrepareTrace(invalid, TraceOptions{InvalidRowPolicy: InvalidRowDrop})
	if err != nil {
		t.Fatal(err)
	}
	if trace.SourceRows != 2 || trace.DroppedRows != 1 || len(trace.Events) != 1 {
		t.Fatalf("drop counts = source %d dropped %d prepared %d", trace.SourceRows, trace.DroppedRows, len(trace.Events))
	}
}

func TestTraceEventIdentityBindsTraceAndCoordinates(t *testing.T) {
	event := ReplayEvent{
		OriginalRow: 7,
		Sequence:    2,
		Source:      ReplayBlock{Chain: "a", OriginalHeight: 11, NormalizedHeight: 1},
		Destination: ReplayBlock{Chain: "b", OriginalHeight: 22, NormalizedHeight: 2},
		SourceTime:  time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC),
	}
	first := ComputeEventID("aa", event)
	event.Source.NormalizedHeight = 999 // audit coordinate is derived and not identity material.
	if second := ComputeEventID("aa", event); second != first {
		t.Fatalf("normalized height changed event identity: %q != %q", second, first)
	}
	event.Source.OriginalHeight++
	if second := ComputeEventID("aa", event); second == first {
		t.Fatal("original height did not change event identity")
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
